#!/usr/bin/env python3
"""Package an already-built Linux amd64 plugin and checksum the ZIP."""
import argparse
import hashlib
import pathlib
import re
import struct
import zipfile

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('version', help='Dotted numeric version without v, e.g. 0.2.0')
p.add_argument('--library', type=pathlib.Path, default=pathlib.Path('dist/codex-ticket.so'))
p.add_argument('--output', type=pathlib.Path, default=pathlib.Path('release'))
a = p.parse_args()
if not re.fullmatch(r'[0-9]+(?:\.[0-9]+)+', a.version):
    p.error('Expected dotted numeric version without v')
data = a.library.read_bytes()
if data[:6] != b'\x7fELF\x02\x01' or struct.unpack_from('<H', data, 18)[0] != 62:
    p.error('Expected a 64-bit little-endian x86_64 ELF library')
a.output.mkdir(parents=True, exist_ok=True)
asset = a.output / f'codex-ticket_{a.version}_linux_amd64.zip'
info = zipfile.ZipInfo('codex-ticket.so', date_time=(2026, 1, 1, 0, 0, 0))
info.create_system = 3
info.external_attr = 0o100755 << 16
info.compress_type = zipfile.ZIP_DEFLATED
with zipfile.ZipFile(asset, 'w') as archive:
    archive.writestr(info, data)
    for filename in ('LICENSE', 'NOTICE', 'THIRD_PARTY_NOTICES.txt'):
        notice = pathlib.Path(filename)
        entry = zipfile.ZipInfo(filename, date_time=(2026, 1, 1, 0, 0, 0))
        entry.compress_type = zipfile.ZIP_DEFLATED
        archive.writestr(entry, notice.read_bytes())
with zipfile.ZipFile(asset) as archive:
    assert [n for n in archive.namelist() if n.endswith('.so')] == ['codex-ticket.so']
    assert archive.read('codex-ticket.so') == data
checksum = hashlib.sha256(asset.read_bytes()).hexdigest()
(a.output / 'checksums.txt').write_text(f'{checksum}  {asset.name}\n')
print(asset)
print(f'SHA256 {checksum}')
