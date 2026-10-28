#!/usr/bin/env python3
"""Build public release metadata and an installer with pinned binary hashes."""
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys

output, revision = Path(sys.argv[1]), sys.argv[2]
version = sys.argv[3] if len(sys.argv) > 3 else ''
if not re.fullmatch(r'[a-f0-9]{40}', revision):
    raise SystemExit('Invalid source revision')
if version and not re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+(?:-rc\.[0-9]+)?', version):
    raise SystemExit('Use vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-rc.N')
platforms = ['darwin-amd64', 'darwin-arm64', 'linux-amd64', 'linux-arm64']
hashes = {f'spr-ceremony-{p}': hashlib.sha256((output / f'spr-ceremony-{p}').read_bytes()).hexdigest() for p in platforms}
metadata = {'source_revision': revision, 'go': subprocess.check_output(['go', 'version'], text=True).strip(), 'sha256': dict(hashes)}
if version:
    template = (Path(__file__).parent / 'join.sh.in').read_text()
    template = template.replace('@VERSION@', version).replace('@REVISION@', revision)
    for platform in platforms:
        template = template.replace('@' + platform.replace('-', '_').upper() + '@', hashes['spr-ceremony-' + platform])
    if re.search(r'@[A-Z_]+@', template):
        raise SystemExit('Unexpanded installer placeholder')
    installer = template.encode()
    (output / 'join.sh').write_bytes(installer)
    hashes['join.sh'] = hashlib.sha256(installer).hexdigest()
    metadata.update(version=version, installer={'sha256': hashes['join.sh'], 'size': len(installer)})
(output / 'build.json').write_text(json.dumps(metadata, indent=2) + '\n')
hashes['build.json'] = hashlib.sha256((output / 'build.json').read_bytes()).hexdigest()
(output / 'SHA256SUMS').write_text(''.join(f'{digest}  {name}\n' for name, digest in sorted(hashes.items())))
