import json, os, pathlib, shutil, sys
request = json.load(sys.stdin)
home = pathlib.Path(os.environ["C2J_OBJECT_OUTBOX"]) / "home"
if "session" in request:
    session = request["session"]
    assert session["metadata"]["session_id"] == "same-id"
    assert pathlib.Path(session["files"]["home"], "rollout").read_text() == request["expected"]
    shutil.copytree(session["files"]["home"], home)
else:
    home.mkdir()
(home / "rollout").write_text(request["text"])
print(json.dumps({"output": {"session": {"$object": "next"}}, "objects": {"next": {"type": "test.session/v1", "metadata": {"session_id": "same-id"}, "files": {"home": str(home)}}}}))
