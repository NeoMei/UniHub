"""Pack only the four runtime directories from a built, patched 1.46.0 checkout."""
import gzip
import hashlib
import os
from pathlib import Path
import sys
import tarfile
import tempfile

source, output = map(Path, sys.argv[1:])
roots = ('packages/core/build', 'packages/console/dist', 'packages/experience/dist-admin', 'packages/account/dist-admin')
for directory in roots:
    entry = 'index.js' if directory.endswith('/build') else 'index.html'
    if not (source / directory / entry).is_file():
        raise SystemExit(f'Missing {directory}/{entry}')
output.mkdir(parents=True, exist_ok=True)
name = 'logto-1.46.0-admin-auth-overlay.tar.gz'
with tempfile.NamedTemporaryFile(dir=output, delete=False) as temporary:
    try:
        with gzip.GzipFile(filename='', mode='wb', fileobj=temporary, mtime=0) as compressed:
            with tarfile.open(fileobj=compressed, mode='w|', format=tarfile.PAX_FORMAT) as archive:
                for directory in roots:
                    for path in sorted((source / directory).rglob('*')):
                        if path.is_symlink():
                            raise ValueError(f'Unexpected link in build: {path}')
                        if not path.is_file() or path.suffix == '.map' or path.name == '.DS_Store':
                            continue
                        entry = tarfile.TarInfo(str(path.relative_to(source)))
                        entry.mode = 0o644
                        entry.size = path.stat().st_size
                        with path.open('rb') as data:
                            archive.addfile(entry, data)
        temporary.flush()
        os.chmod(temporary.name, 0o644)
        os.replace(temporary.name, output / name)
    finally:
        Path(temporary.name).unlink(missing_ok=True)
with (output / name).open('rb') as payload:
    digest = hashlib.sha256()
    for chunk in iter(lambda: payload.read(1 << 20), b''):
        digest.update(chunk)
(output / 'SHA256SUMS').write_text(f'{digest.hexdigest()}  {name}\n')
print(name, digest.hexdigest())
