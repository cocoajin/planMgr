#!/usr/bin/env python3
"""Build a standard Debian ar package without requiring dpkg on macOS."""
import pathlib,tarfile,io,time,hashlib
root=pathlib.Path(__file__).resolve().parents[1];dist=root/'dist'
def tgz(entries):
 b=io.BytesIO()
 with tarfile.open(fileobj=b,mode='w:gz') as t:
  for name,data,mode in entries:
   info=tarfile.TarInfo(name);info.mode=mode;info.size=len(data);info.uid=info.gid=0;info.mtime=0;t.addfile(info,io.BytesIO(data))
 return b.getvalue()
def member(name,data):
 header=f'{name+"/":<16}{0:<12}{0:<6}{0:<6}{"100644":<8}{len(data):<10}`\n'.encode()
 return header+data+(b'\n' if len(data)%2 else b'')
for arch in ['amd64','arm64']:
 stage=dist/f'planMgr-go-linux-{arch}'
 control=f'''Package: planmgr-go
Version: 1.0.0
Section: admin
Priority: optional
Architecture: {arch}
Maintainer: PlanMgr maintainers <admin@localhost>
Depends: bash, systemd
Description: PlanMgr web-based scheduled task manager
 Go and SQLite scheduler with embedded web UI.
'''
 postinst='''#!/bin/sh
set -e
if [ "$1" = configure ]; then
 mkdir -p /var/lib/planmgr-go
 chmod 700 /var/lib/planmgr-go
 systemctl daemon-reload
 systemctl enable PlanMgrGo.service
 systemctl restart PlanMgrGo.service
fi
'''
 prerm='''#!/bin/sh
set -e
if [ "$1" = remove ] || [ "$1" = upgrade ]; then systemctl stop PlanMgrGo.service || true; fi
if [ "$1" = remove ]; then systemctl disable PlanMgrGo.service || true; fi
'''
 postrm='''#!/bin/sh
set -e
systemctl daemon-reload || true
if [ "$1" = purge ]; then rm -rf /var/lib/planmgr-go; fi
'''
 unit='''[Unit]
Description=PlanMgr task manager
After=network.target
[Service]
Type=simple
ExecStart=/opt/planmgr-go/planmgr --data-dir /var/lib/planmgr-go
WorkingDirectory=/opt/planmgr-go
Restart=on-failure
User=root
UMask=0077
[Install]
WantedBy=multi-user.target
'''
 entries=[('opt/planmgr-go/planmgr',(stage/'planmgr').read_bytes(),0o755),('usr/lib/systemd/system/PlanMgrGo.service',unit.encode(),0o644),('usr/share/doc/planmgr-go/README.md',(root/'README.md').read_bytes(),0o644)]
 for f in (stage/'licenses').iterdir():entries.append(('usr/share/doc/planmgr-go/licenses/'+f.name,f.read_bytes(),0o644))
 result=b'!<arch>\n'+member('debian-binary',b'2.0\n')+member('control.tar.gz',tgz([('control',control.encode(),0o644),('postinst',postinst.encode(),0o755),('prerm',prerm.encode(),0o755),('postrm',postrm.encode(),0o755)]))+member('data.tar.gz',tgz(entries))
 out=dist/f'planmgr-go_1.0.0_{arch}.deb';out.write_bytes(result);print(out)
checks=[]
for f in sorted(dist.iterdir()):
 if f.is_file() and f.suffix in ['.zip','.gz','.deb']:checks.append(hashlib.sha256(f.read_bytes()).hexdigest()+'  '+f.name)
(dist/'SHA256SUMS').write_text('\n'.join(checks)+'\n')
