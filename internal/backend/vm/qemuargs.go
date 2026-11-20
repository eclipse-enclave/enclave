// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package vm

import (
	"fmt"
	"strconv"

	"enclave/internal/backend"
	"enclave/internal/backend/vmhost"
)

const (
	qemuBinary    = "qemu-system-x86_64"
	qemuImgBinary = "qemu-img"

	// q35 puts the host bridge in slot 0 and the ICH9 devices in slot 0x1f.
	pciFirstSlot        = 0x01
	pciLastSlot         = 0x1e
	pciFunctionsPerSlot = 8
)

// machine is one QEMU invocation.
type machine struct {
	Name      string
	MemoryMiB int
	CPUs      int
	Disk      string
	// Seed is a NoCloud seed image, attached only to provision a base.
	Seed    string
	Shares  []share
	Hostfwd []string
	Console string
	// QMP, Control and PIDFile are set for a session, which runs daemonized.
	// A provisioning boot stays in the foreground until the guest powers off.
	// A QEMU monitor serves one client at a time: QMP is for the process that
	// started the VM and watches it, Control for a later `stop`.
	QMP     string
	Control string
	PIDFile string
	// socket returns the virtiofsd socket of the share at an index.
	socket func(index int) string
}

func qemuArgs(m machine) ([]string, error) {
	for _, option := range [][2]string{{"session name", m.Name}, {"disk", m.Disk}, {"seed", m.Seed}, {"console log", m.Console}, {"QMP socket", m.QMP}, {"control socket", m.Control}} {
		if err := vmhost.RejectOptionComma(backend.NameVM, option[0], option[1]); err != nil {
			return nil, err
		}
	}
	devices := 2 + len(m.Shares)
	if m.Seed != "" {
		devices++
	}
	addrs, err := pciAddrs(devices)
	if err != nil {
		return nil, err
	}
	nextAddr := func() string {
		addr := addrs[0]
		addrs = addrs[1:]
		return addr
	}
	memory := strconv.Itoa(m.MemoryMiB)
	netdev := "user,id=net0"
	for _, forward := range m.Hostfwd {
		netdev += ",hostfwd=" + forward
	}
	args := []string{
		"-nodefaults",
		"-no-user-config",
		"-name", m.Name,
		"-machine", "q35,accel=kvm",
		"-cpu", "host",
		"-smp", strconv.Itoa(m.CPUs),
		"-m", memory,
		// vhost-user-fs needs guest memory the daemons can map; the backend
		// size has to equal -m.
		"-object", "memory-backend-memfd,id=mem,size=" + memory + "M,share=on",
		"-numa", "node,memdev=mem",
		"-display", "none",
		"-serial", "file:" + m.Console,
		"-drive", "file=" + m.Disk + ",if=none,id=disk0,format=qcow2,discard=unmap",
		"-device", "virtio-blk-pci,drive=disk0,bootindex=1" + nextAddr(),
		"-netdev", netdev,
		"-device", "virtio-net-pci,netdev=net0" + nextAddr(),
	}
	if m.Seed != "" {
		args = append(args,
			"-drive", "file="+m.Seed+",if=none,id=seed0,format=raw,readonly=on",
			"-device", "virtio-blk-pci,drive=seed0"+nextAddr(),
		)
	}
	for i, sh := range m.Shares {
		if err := vmhost.RejectOptionComma(backend.NameVM, "mount source", sh.Source); err != nil {
			return nil, err
		}
		id := "fs" + strconv.Itoa(i)
		switch sh.Transport {
		case transportVirtiofs:
			socket := m.socket(i)
			if err := vmhost.RejectOptionComma(backend.NameVM, "virtiofsd socket", socket); err != nil {
				return nil, err
			}
			args = append(args,
				"-chardev", "socket,id="+id+",path="+socket,
				"-device", "vhost-user-fs-pci,chardev="+id+",tag="+sh.Tag+nextAddr(),
			)
		case transport9P:
			fsdev := "local,id=" + id + ",path=" + sh.Source + ",security_model=none"
			if sh.ReadOnly {
				fsdev += ",readonly=on"
			}
			args = append(args,
				"-fsdev", fsdev,
				"-device", "virtio-9p-pci,fsdev="+id+",mount_tag="+sh.Tag+nextAddr(),
			)
		default:
			return nil, fmt.Errorf("vm backend: unknown share transport %q", sh.Transport)
		}
	}
	if m.QMP != "" {
		args = append(args,
			"-qmp", "unix:"+m.QMP+",server=on,wait=off",
			"-qmp", "unix:"+m.Control+",server=on,wait=off",
			"-pidfile", m.PIDFile, "-daemonize")
	}
	return args, nil
}

// pciAddrs returns the address suffix of each device. One device per slot
// runs out after 30; a session has one export per mount and store, so the
// functions of each slot are used too.
func pciAddrs(count int) ([]string, error) {
	capacity := (pciLastSlot - pciFirstSlot + 1) * pciFunctionsPerSlot
	if count > capacity {
		return nil, fmt.Errorf("vm backend: %d devices exceed the %d PCI functions of the machine", count, capacity)
	}
	addrs := make([]string, count)
	for i := range addrs {
		function := i % pciFunctionsPerSlot
		addrs[i] = fmt.Sprintf(",addr=0x%x.0x%x", pciFirstSlot+i/pciFunctionsPerSlot, function)
		// Guests scan the other functions of a slot only when function 0
		// carries the multifunction bit.
		if function == 0 && i+1 < count {
			addrs[i] += ",multifunction=on"
		}
	}
	return addrs, nil
}
