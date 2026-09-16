#!/usr/bin/env python3
import os,subprocess,pathlib,zipfile,tarfile,shutil,hashlib,json,argparse
root=pathlib.Path(__file__).resolve().parents[1];dist=root/'dist';dist.mkdir(exist_ok=True)
parser=argparse.ArgumentParser(description='构建指定平台发行包，默认仅 macOS arm64')
parser.add_argument('--target', choices=['darwin-arm64','darwin-amd64','linux-amd64','linux-arm64','all'], default='darwin-arm64')
args=parser.parse_args()
targets=[('darwin','arm64'),('darwin','amd64'),('linux','amd64'),('linux','arm64')] if args.target=='all' else [tuple(args.target.split('-'))]
go=os.environ.get('GO','go')
# Keep dependency license texts beside shipped binaries.
mods=subprocess.check_output([go,'list','-m','-json','all'],cwd=root,text=True)
dec=json.JSONDecoder();remaining=mods;module_info=[]
while remaining.strip():
 m,n=dec.raw_decode(remaining.lstrip());module_info.append(m);remaining=remaining.lstrip()[n:]
for target,arch in targets:
 name=f'planMgr-go-{target}-{arch}';stage=dist/name
 if stage.exists():shutil.rmtree(stage)
 stage.mkdir();env=os.environ.copy();env.update(GOOS=target,GOARCH=arch,CGO_ENABLED='0')
 subprocess.run([go,'build','-trimpath','-ldflags=-s -w','-o',str(stage/'planmgr'),'.'],cwd=root,env=env,check=True)
 shutil.copy2(root/'README.md',stage/'README.md')
 for image in ['img1.png','img2.png']:shutil.copy2(root/image,stage/image)
 files=['安装并启动.command','卸载并清理数据.command'] if target=='darwin' else ['install-ubuntu.sh','uninstall-ubuntu.sh']
 for f in files:shutil.copy2(root/'packaging'/f,stage/f)
 licenses=stage/'licenses';licenses.mkdir()
 for m in module_info:
  if m.get('Main') or not m.get('Dir'):continue
  for f in pathlib.Path(m['Dir']).iterdir():
   if f.is_file() and (f.name.upper().startswith('LICENSE') or f.name.upper().startswith('COPYING')):
    shutil.copy2(f,licenses/(m['Path'].replace('/','_')+'-'+f.name))
 (licenses/'modules.json').write_text(json.dumps([{k:m[k] for k in ['Path','Version','Sum'] if k in m} for m in module_info],indent=2))
 if target=='darwin':
  archive=dist/(name+'.zip')
  with zipfile.ZipFile(archive,'w',zipfile.ZIP_DEFLATED) as z:
   for f in stage.rglob('*'):
    if f.is_file():z.write(f,pathlib.Path(name)/f.relative_to(stage))
 else:
  archive=dist/(name+'.tar.gz')
  with tarfile.open(archive,'w:gz') as t:t.add(stage,arcname=name)
 print(archive,flush=True)
checks=[]
for f in sorted(dist.iterdir()):
 if f.is_file() and (f.name.endswith('.zip') or f.name.endswith('.tar.gz')):checks.append(hashlib.sha256(f.read_bytes()).hexdigest()+'  '+f.name)
(dist/'SHA256SUMS').write_text('\n'.join(checks)+'\n')
