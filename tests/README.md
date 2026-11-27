# Testing Farm tests

## Run with a virtual machine

From the `tests` directory, run:

```bash
tmt run
```

By default, tmt provisions virtual machines for the plans. This requires a
working tmt virtual provisioner and a package repository available to the
guests that provides the Enclave RPM. To enable the Enclave COPR in the guests
before installing dependencies, run:

```bash
tmt run -av prepare -h install --insert --order 10 \
  --copr filippor/enclave
```

The COPR package may not match the current checkout. Container images are built
in the smoke plan's guest and are not reused across fresh runs.

## Run the smoke plan locally

Run the smoke plan as a non-root user on Fedora, preferably in a disposable VM
with tmt, rootless Podman, and the Enclave RPM installed. The tests use
`/usr/bin/enclave`, not a binary built in the checkout. The local provision
method runs tests on the machine and may install missing dependencies.

From the repository root:

```bash
cd tests
mkdir -p /var/tmp/enclave-tmt-cache-$UID

tmt --allow-unsafe-behavior=provision/local run -av \
  -e ENCLAVE_TMT_PODMAN_DATA_DIR=/var/tmp/enclave-tmt-cache-$UID \
  plan -n '^/plans/smoke$' provision -h local
```

The first run builds the container images. Later runs reuse them from the
specified directory unless the image build inputs change. The cache is used only
for non-root runs; root runs use a temporary user and fresh storage.
