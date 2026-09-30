import argparse
import base64
import hashlib
import json
import os
import platform
import plistlib
import shutil
import subprocess
import sys
import tarfile
from pathlib import Path

from pilot import state_lock

HERE = Path(__file__).resolve().parent


def run(*argv, **kwargs):
    subprocess.run([str(x) for x in argv], check=True, **kwargs)


def download(asset, path):
    run("curl", "-fsSL", "--proto", "=https", "--proto-redir", "=https", "--max-time", "180",
        asset["url"], "-o", path)
    data = path.read_bytes()
    if "sha256" in asset:
        valid = hashlib.sha256(data).hexdigest() == asset["sha256"]
    else:
        valid = asset["integrity"] == "sha512-" + base64.b64encode(hashlib.sha512(data).digest()).decode()
    if not valid:
        path.unlink()
        raise RuntimeError("artifact checksum mismatch")


def extract_binary(archive, name, destination):
    with tarfile.open(archive) as stream:
        matches = [entry for entry in stream.getmembers() if entry.isfile() and Path(entry.name).name == name]
        if len(matches) != 1:
            raise RuntimeError("ambiguous artifact contents")
        with stream.extractfile(matches[0]) as source, destination.open("wb") as output:
            shutil.copyfileobj(source, output)
    destination.chmod(0o755)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--state", type=Path, required=True)
    parser.add_argument("--runtime", choices=["docker", "vm"], default="docker")
    parser.add_argument("--vm-driver", type=Path)
    args = parser.parse_args()
    if args.vm_driver and (args.runtime != "vm" or not args.vm_driver.is_file()):
        parser.error("--vm-driver requires --runtime vm and a locally built driver binary")
    try:
        with state_lock(args.state):
            prepare(args)
    except (RuntimeError, OSError) as error:
        print(str(error), file=sys.stderr)
        return 1
    return 0


def prepare(args):
    state = args.state.resolve()
    if (state / "checkpoint.json").exists():
        raise RuntimeError("prepare requires fresh state; an existing checkpoint must not be overwritten")
    state.mkdir(mode=0o700, parents=True, exist_ok=True)
    state.chmod(0o700)
    os.umask(0o077)
    machine = {"arm64": "aarch64", "aarch64": "aarch64", "x86_64": "x86_64"}[platform.machine()]
    target = machine + ("-apple-darwin" if platform.system() == "Darwin" else "-unknown-linux")
    assets = json.loads((HERE / "artifacts.lock.json").read_text())["assets"]
    (state / "bin").mkdir(exist_ok=True)
    (state / "proto").mkdir(exist_ok=True)
    (state / "generated").mkdir(exist_ok=True)
    binaries = ["openshell", "openshell-gateway", "openshell-prover"]
    if args.runtime == "vm":
        binaries.append("openshell-driver-vm")
    for binary in binaries:
        suffix = "" if platform.system() == "Darwin" else ("-gnu" if binary in ["openshell-gateway", "openshell-driver-vm"] else "-musl")
        name = f"{binary}-{target}{suffix}.tar.gz"
        download(assets[name], state / name)
        extract_binary(state / name, binary, state / "bin" / binary)
    if args.vm_driver:
        shutil.copyfile(args.vm_driver, state / "bin/openshell-driver-vm")
        (state / "bin/openshell-driver-vm").chmod(0o755)
    if args.runtime == "vm" and platform.system() == "Darwin":
        entitlements = state / "vm-entitlements.plist"
        entitlements.write_bytes(plistlib.dumps({"com.apple.security.hypervisor": True}))
        run("/usr/bin/codesign", "--entitlements", entitlements, "--force", "-s", "-",
            state / "bin/openshell-driver-vm")
    for name in ["extension.proto", "supervisor_middleware.proto"]:
        download(assets[name], state / "proto" / name)
    run("uv", "venv", "--python", "3.12", state / "venv")
    python = state / "venv/bin/python"
    run("uv", "pip", "install", "--python", python, "--require-hashes", "--only-binary", ":all:",
        "--index-url", "https://pypi.org/simple", "-r", HERE / "requirements.txt")
    include = subprocess.check_output([str(python), "-c", "import grpc_tools,pathlib; print(pathlib.Path(grpc_tools.__file__).parent/'_proto')"], text=True).strip()
    run(python, "-m", "grpc_tools.protoc", f"-I{state / 'proto'}", f"-I{include}",
        f"--python_out={state / 'generated'}", f"--grpc_python_out={state / 'generated'}",
        state / "proto/extension.proto", state / "proto/supervisor_middleware.proto")
    run("go", "build", "-o", state / "shield", "./examples/openshell-pilot/shield", cwd=HERE.parents[1])
    build = state / "image"
    build.mkdir(exist_ok=True)
    arch = "arm64" if machine == "aarch64" else "x64"
    download(assets[f"opencode-linux-{arch}"], state / "opencode.tgz")
    extract_binary(state / "opencode.tgz", "opencode", build / "opencode")
    shutil.copyfile(HERE / "Dockerfile", build / "Dockerfile")
    run("docker", "build", "--tag", "captain-openshell-pilot:2", build)
    print("Prepared pinned OpenShell, OpenCode, Shield and optional middleware dependencies.")


if __name__ == "__main__":
    raise SystemExit(main())
