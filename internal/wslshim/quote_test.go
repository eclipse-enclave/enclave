// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package wslshim

import (
	"reflect"
	"strings"
	"testing"
)

const testExe = `C:\Windows\System32\wsl.exe`

// goldenArgs is the argument shapes enclave actually has to survive: prompts
// with spaces and quotes, agent flags after --, Windows and POSIX variable
// syntax that must not be expanded, and non-ASCII text.
var goldenArgs = []struct {
	name string
	arg  string
	want string
}{
	{"plain", "continue", "continue"},
	{"flag", "--tool", "--tool"},
	{"bare double dash", "--", "--"},
	{"empty", "", `""`},
	{"space", "hello world", `"hello world"`},
	{"tab", "a\tb", "\"a\tb\""},
	{"newline", "a\nb", "\"a\nb\""},
	{"vertical tab", "a\vb", "\"a\vb\""},
	{"double quote", `say "hi"`, `"say \"hi\""`},
	{"quote only", `"`, `"\""`},
	{"backslash without space", `a\b`, `a\b`},
	{"backslash with space", `a\b c`, `"a\b c"`},
	{"trailing backslash", `dir\ `, `"dir\ "`},
	{"trailing backslash at end", `C:\dir\`, `C:\dir\`},
	{"trailing backslash quoted", `C:\my dir\`, `"C:\my dir\\"`},
	{"backslash before quote", `a\"b`, `"a\\\"b"`},
	{"backslashes before quote with space", `a\\" b`, `"a\\\\\" b"`},
	{"windows variable", "%USERPROFILE%", "%USERPROFILE%"},
	{"windows variable with space", "%USERPROFILE% and more", `"%USERPROFILE% and more"`},
	{"posix variable", "$HOME/project", "$HOME/project"},
	{"posix variable braced", "${HOME}", "${HOME}"},
	{"shell metacharacters", "a&b|c>d<e^f", "a&b|c>d<e^f"},
	{"single quotes", "it's fine", `"it's fine"`},
	{"emoji", "ship it 🚀", `"ship it 🚀"`},
	{"cjk", "説明を書いて", "説明を書いて"},
	{"prompt", `refactor the "auth" module\ into internal/auth`, `"refactor the \"auth\" module\ into internal/auth"`},
}

func TestEscapeArgGolden(t *testing.T) {
	for _, tc := range goldenArgs {
		t.Run(tc.name, func(t *testing.T) {
			if got := escapeArg(tc.arg); got != tc.want {
				t.Errorf("escapeArg(%q) = %q, want %q", tc.arg, got, tc.want)
			}
		})
	}
}

// TestBuildCommandLineRoundTrip is the check that matters: every golden
// argument must come back out of a Windows-rules parser exactly as it went in.
func TestBuildCommandLineRoundTrip(t *testing.T) {
	for _, tc := range goldenArgs {
		t.Run(tc.name, func(t *testing.T) {
			assertRoundTrip(t, testExe, []string{tc.arg})
		})
	}
}

func TestBuildCommandLineRoundTripFullInvocation(t *testing.T) {
	cases := [][]string{
		{"-d", "Ubuntu", "--cd", "/home/p/my project", "-e", "/usr/bin/enclave"},
		{"-e", "/usr/bin/enclave", "--tool", "claude", "--", "-p", `write a "hello world" in C:\tmp\`},
		{"-e", "/bin/sh", "-c", cdScript, "enclave-wsl-launcher", "/home/p/proj", "/usr/bin/enclave", ""},
		{"-e", "/bin/sh", "-lc", probeScript},
	}
	for _, args := range cases {
		assertRoundTrip(t, testExe, args)
	}
}

func TestBuildCommandLineRoundTripExeWithSpaces(t *testing.T) {
	assertRoundTrip(t, `C:\Program Files\WSL\wsl.exe`, []string{"-d", "Ubuntu-24.04"})
}

func TestBuildCommandLineIncludesExeFirst(t *testing.T) {
	cmdLine, err := buildCommandLine(testExe, []string{"-d", "Ubuntu"})
	if err != nil {
		t.Fatalf("buildCommandLine: %v", err)
	}
	want := testExe + " -d Ubuntu"
	if cmdLine != want {
		t.Errorf("buildCommandLine = %q, want %q", cmdLine, want)
	}
}

func TestBuildCommandLineRejectsOverlongLine(t *testing.T) {
	// Just past the limit once argv[0] and the separator are counted.
	long := strings.Repeat("a", maxCommandLine)
	if _, err := buildCommandLine(testExe, []string{long}); err == nil {
		t.Fatal("expected an error for a command line past the CreateProcess limit")
	}
}

func TestBuildCommandLineAcceptsLineJustUnderLimit(t *testing.T) {
	fixed := len(testExe) + 1 // argv[0] plus the separating space
	long := strings.Repeat("a", maxCommandLine-fixed-1)

	cmdLine, err := buildCommandLine(testExe, []string{long})
	if err != nil {
		t.Fatalf("buildCommandLine: %v", err)
	}
	if got := utf16Len(cmdLine); got != maxCommandLine-1 {
		t.Errorf("command line length = %d, want %d", got, maxCommandLine-1)
	}
	assertRoundTrip(t, testExe, []string{long})
}

func TestBuildCommandLineCountsSurrogatePairs(t *testing.T) {
	// An emoji is one rune but two UTF-16 code units, and Windows counts the
	// units. Half the limit's worth of emoji must therefore be rejected.
	emoji := strings.Repeat("🚀", maxCommandLine/2)
	if _, err := buildCommandLine(testExe, []string{emoji}); err == nil {
		t.Fatal("expected an error: surrogate pairs cost two UTF-16 code units each")
	}
}

func TestUTF16Len(t *testing.T) {
	cases := map[string]int{
		"":      0,
		"abc":   3,
		"é":     1,
		"日本":    2,
		"🚀":     2,
		"a🚀b":   4,
		"\x00a": 2,
	}
	for input, want := range cases {
		if got := utf16Len(input); got != want {
			t.Errorf("utf16Len(%q) = %d, want %d", input, got, want)
		}
	}
}

func assertRoundTrip(t *testing.T, exe string, args []string) {
	t.Helper()

	cmdLine, err := buildCommandLine(exe, args)
	if err != nil {
		t.Fatalf("buildCommandLine(%q, %q): %v", exe, args, err)
	}

	got := parseWindowsCommandLine(cmdLine)
	want := append([]string{exe}, args...)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip of %q\n  command line %q\n  got  %q\n  want %q", args, cmdLine, got, want)
	}
}

// parseWindowsCommandLine reimplements CommandLineToArgvW so the escaping can be
// verified without a Windows host. The rules are Microsoft's: argv[0] is
// delimited by quotes with backslashes taken literally, and for the remaining
// arguments a run of 2n backslashes before a quote yields n backslashes and a
// quote that toggles the quoted state, while 2n+1 yields n backslashes and a
// literal quote.
//
// The one rule left out is the doubled quote inside a quoted argument, where the
// real parser yields a literal quote and stays quoted. escapeArg only ever emits
// the \" form, so no command line this package produces can reach it.
func parseWindowsCommandLine(cmdLine string) []string {
	var argv []string
	i := 0

	// argv[0].
	var exe strings.Builder
	if i < len(cmdLine) && cmdLine[i] == '"' {
		i++
		for i < len(cmdLine) && cmdLine[i] != '"' {
			exe.WriteByte(cmdLine[i])
			i++
		}
		i++ // closing quote
	} else {
		for i < len(cmdLine) && cmdLine[i] != ' ' && cmdLine[i] != '\t' {
			exe.WriteByte(cmdLine[i])
			i++
		}
	}
	argv = append(argv, exe.String())

	var current strings.Builder
	inArg, quoted, backslashes := false, false, 0

	flush := func() {
		current.WriteString(strings.Repeat(`\`, backslashes))
		backslashes = 0
	}

	for ; i < len(cmdLine); i++ {
		switch c := cmdLine[i]; {
		case c == '\\':
			backslashes++
			inArg = true
		case c == '"':
			current.WriteString(strings.Repeat(`\`, backslashes/2))
			if backslashes%2 == 1 {
				current.WriteByte('"')
			} else {
				quoted = !quoted
			}
			backslashes = 0
			inArg = true
		case (c == ' ' || c == '\t') && !quoted:
			flush()
			if inArg {
				argv = append(argv, current.String())
				current.Reset()
				inArg = false
			}
		default:
			flush()
			current.WriteByte(c)
			inArg = true
		}
	}
	flush()
	if inArg {
		argv = append(argv, current.String())
	}
	return argv
}

// TestParseWindowsCommandLineReference guards the reference parser itself, so a
// round-trip pass cannot come from both sides being wrong in the same way.
func TestParseWindowsCommandLineReference(t *testing.T) {
	cases := []struct {
		cmdLine string
		want    []string
	}{
		{`wsl.exe a b`, []string{"wsl.exe", "a", "b"}},
		{`wsl.exe  a   b `, []string{"wsl.exe", "a", "b"}},
		{`wsl.exe "a b"`, []string{"wsl.exe", "a b"}},
		{`wsl.exe ""`, []string{"wsl.exe", ""}},
		{`wsl.exe a\b`, []string{"wsl.exe", `a\b`}},
		{`wsl.exe "a\\b"`, []string{"wsl.exe", `a\\b`}},
		{`wsl.exe \"a\"`, []string{"wsl.exe", `"a"`}},
		{`wsl.exe "a\"b"`, []string{"wsl.exe", `a"b`}},
		{`wsl.exe "C:\dir\\"`, []string{"wsl.exe", `C:\dir\`}},
		{`"C:\Program Files\wsl.exe" -d Ubuntu`, []string{`C:\Program Files\wsl.exe`, "-d", "Ubuntu"}},
	}
	for _, tc := range cases {
		if got := parseWindowsCommandLine(tc.cmdLine); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseWindowsCommandLine(%q) = %q, want %q", tc.cmdLine, got, tc.want)
		}
	}
}
