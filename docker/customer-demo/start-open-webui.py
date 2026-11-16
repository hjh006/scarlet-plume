import os
from pathlib import Path
import socket
import stat


# Keep the official application and start script unchanged; pass it a private
# pre-bound socket so Uvicorn cannot reset its permissions to a public mode.
os.umask(0o077)
path = Path("/run/open-webui/webui.sock")
if path.exists():
    if not stat.S_ISSOCK(path.lstat().st_mode):
        raise RuntimeError("refusing to replace a non-socket entry")
    path.unlink()
listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
listener.bind(str(path))
path.chmod(0o600)
os.set_inheritable(listener.fileno(), True)
os.execvpe("/bin/bash", ["bash", "/app/backend/start.sh", "--fd", str(listener.fileno())], os.environ)
