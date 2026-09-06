"""Validate release tags and package the four supported native binaries."""

import argparse
import hashlib
from pathlib import Path
import re
import tarfile
import zipfile


TARGETS = ("linux-arm64", "linux-amd64", "windows-amd64", "macos-arm64")


def version(tag: str) -> str:
    number = r"(0|[1-9][0-9]*)"
    match = re.fullmatch(
        rf"v{number}\.{number}\.{number}(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?",
        tag,
    )
    if match is None:
        raise ValueError("expected a tag such as v1.0.0 or v1.0.0-rc.1")
    prerelease = match.group(4)
    if prerelease and any(
        part.isdigit() and len(part) > 1 and part.startswith("0")
        for part in prerelease.split(".")
    ):
        raise ValueError("numeric prerelease identifiers cannot have leading zeros")
    return tag[1:]


def archive_name(tag: str, target: str) -> str:
    version(tag)
    if target not in TARGETS:
        raise ValueError(f"unsupported release target: {target}")
    extension = "zip" if target == "windows-amd64" else "tar.gz"
    return f"ash-{tag}-{target}.{extension}"


def package(tag: str, target: str, binary: Path, readme: Path,
            notices: Path, output: Path) -> Path:
    name = archive_name(tag, target)
    executable = "ash.exe" if target == "windows-amd64" else "ash"
    files = [(binary, executable), (readme, "README.md")]
    for notice in sorted(notices.rglob("*")):
        if notice.is_file() and any(
            word in notice.name.lower()
            for word in ("license", "notice", "copyright", "copying")
        ):
            files.append((notice, "licenses/" + notice.relative_to(notices).as_posix()))
    if len(files) == 2:
        raise ValueError("dependency license notices are required")
    for source, _ in files:
        if not source.is_file():
            raise ValueError(f"missing release input: {source}")
    output.mkdir(parents=True, exist_ok=True)
    destination = output / name
    if target == "windows-amd64":
        with zipfile.ZipFile(destination, "w", zipfile.ZIP_DEFLATED) as archive:
            for source, member in files:
                archive.write(source, member)
    else:
        with tarfile.open(destination, "w:gz") as archive:
            for source, member in files:
                info = archive.gettarinfo(str(source), arcname=member)
                info.mode = 0o755 if member == executable else 0o644
                info.uid = info.gid = 0
                info.uname = info.gname = ""
                with source.open("rb") as content:
                    archive.addfile(info, content)
    return destination


def checksums(tag: str, directory: Path) -> Path:
    expected = sorted(archive_name(tag, target) for target in TARGETS)
    actual = sorted(p.name for p in directory.iterdir() if p.name != "SHA256SUMS.txt")
    if actual != expected:
        raise ValueError("release must contain exactly the four expected platform archives")
    lines = []
    for name in expected:
        with (directory / name).open("rb") as content:
            digest = hashlib.file_digest(content, "sha256").hexdigest()
        lines.append(f"{digest}  {name}\n")
    destination = directory / "SHA256SUMS.txt"
    destination.write_text("".join(lines), encoding="utf-8", newline="\n")
    return destination


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    validate = commands.add_parser("version")
    validate.add_argument("tag")
    pack = commands.add_parser("package")
    pack.add_argument("tag")
    pack.add_argument("target", choices=TARGETS)
    pack.add_argument("binary", type=Path)
    pack.add_argument("--readme", type=Path, default=Path("README.md"))
    pack.add_argument("--notices", type=Path, default=Path(".deps/notices"))
    pack.add_argument("--output", type=Path, default=Path("dist"))
    sums = commands.add_parser("checksums")
    sums.add_argument("tag")
    sums.add_argument("directory", type=Path)
    args = parser.parse_args()
    try:
        if args.command == "version":
            print(version(args.tag))
        elif args.command == "package":
            print(package(args.tag, args.target, args.binary, args.readme,
                          args.notices, args.output))
        else:
            print(checksums(args.tag, args.directory))
    except (ValueError, OSError) as error:
        parser.error(str(error))


if __name__ == "__main__":
    main()
