"""Package the extension into Vite's existing public directory, using stdlib."""
from pathlib import Path
from zipfile import ZipFile, ZIP_DEFLATED

source = Path(__file__).resolve().parent
target = source.parents[1] / "governance" / "static" / "extensions" / "browser-field-labeler.zip"
target.parent.mkdir(parents=True, exist_ok=True)
files = ["manifest.json", "collect.js", "popup.html", "popup.css", "popup.js", "README.md", "package.json", "test.mjs", "pack.py"]
with ZipFile(target, "w", ZIP_DEFLATED) as archive:
    for name in files:
        archive.write(source / name, "browser-field-labeler/" + name)
with ZipFile(target) as archive:
    assert archive.testzip() is None
print("Packaged browser field labeler into governance/static/extensions")
