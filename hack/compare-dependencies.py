#!/usr/bin/env python3
"""What the panel lists for content against what the image downloads itself.

For each content set in compare-dependencies.json, against the real
Modrinth and the real itzg/minecraft-server image, in plain docker:

  image  the server's own Modrinth entries, MODRINTH_DOWNLOAD_DEPENDENCIES=required
  panel  what the panel lists for them (TestCompareWithImage), with =none

Both are started until the game is Done, and the jars each ends up with
are compared by mod ID and version. Allowed: the panel's is newer, or the
panel does not download what a jar on the server provides already. Then the
panel's server goes through the plan's steps (a mod added, updated, taken
off, a file that provided something taken away, a mod added while Modrinth
is down), started each time. Prints a Markdown report.

Run by compare-dependencies.sh; see there.
"""
import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import time
import urllib.request
import zipfile

UA = {"User-Agent": "GT-Drehmal/avSYS compare-dependencies"}
TYPES = {"fabric": "FABRIC", "quilt": "QUILT", "paper": "PAPER", "neoforge": "NEOFORGE", "forge": "FORGE"}


def get(url):
    return json.load(urllib.request.urlopen(urllib.request.Request(url, headers=UA), timeout=60))


def download(url, dest):
    os.makedirs(os.path.dirname(dest), exist_ok=True)
    with urllib.request.urlopen(urllib.request.Request(url, headers=UA), timeout=300) as r, open(dest, "wb") as f:
        shutil.copyfileobj(r, f)


def lay_uploads(plan, folder):
    """A plan's own files: a version's file from Modrinth, under another name;
    with "fork", made a fork of it, its version so marked in fabric.mod.json,
    as a build of its own that Modrinth does not have."""
    for u in plan.get("uploads", []):
        v = get(f"https://api.modrinth.com/v2/version/{u['version']}")
        f = next((f for f in v["files"] if f["primary"]), v["files"][0])
        dest = os.path.join(folder, u["as"])
        download(f["url"], dest)
        if u.get("fork"):
            with zipfile.ZipFile(dest) as zin:
                items = [(i, zin.read(i.filename)) for i in zin.infolist()]
            with zipfile.ZipFile(dest + ".new", "w", zipfile.ZIP_DEFLATED) as zout:
                for info, data in items:
                    if info.filename == "fabric.mod.json":
                        m = json.loads(data)
                        m["version"] = m.get("version", "") + u["fork"]
                        data = json.dumps(m).encode()
                    zout.writestr(info, data)
            os.replace(dest + ".new", dest)


def start(name, folder, plan, projects, deps, image, log):
    """Starts the game until Done, or until it stops or 10 minutes pass; up to
    three times, as a start can fail on the network: Mojang's and the loaders'
    sites now and then give no answer (see docs/architecture.md, Asking
    Mojang). Each try's log is kept."""
    for attempt in range(3):
        result = start_once(name, folder, plan, projects, deps, image, log + (f".{attempt + 1}" if attempt else ""))
        if result == "Done":
            break
    return result if attempt == 0 else f"{result} (try {attempt + 1})"


def start_once(name, folder, plan, projects, deps, image, log):
    subprocess.run(["docker", "rm", "-f", name], capture_output=True)
    cmd = ["docker", "run", "-d", "--name", name, "--cpus=3", "-m", "5g",
           "-e", "EULA=TRUE", "-e", "TYPE=" + TYPES[plan["loader"]], "-e", "VERSION=" + plan["version"], "-e", "MEMORY=3G",
           "-e", "MODRINTH_PROJECTS=" + "\n".join(projects), "-e", "MODRINTH_DOWNLOAD_DEPENDENCIES=" + deps,
           "-v", os.path.abspath(folder) + ":/data", image]
    subprocess.run(cmd, check=True, capture_output=True)
    result, deadline = "timed out", time.time() + 600
    while time.time() < deadline:
        time.sleep(5)
        got = subprocess.run(["docker", "logs", name], capture_output=True, text=True)
        logs = got.stdout + got.stderr
        if re.search(r"Done \(\d", logs):
            result = "Done"
            break
        running = subprocess.run(["docker", "inspect", "-f", "{{.State.Running}}", name], capture_output=True, text=True).stdout.strip()
        if running != "true":
            last = [l for l in logs.splitlines() if re.search(r"ERROR|Exception|Incompatible|requires", l)]
            result = "failed: " + (re.sub(r"\x1b\[[0-9;]*m", "", last[0])[:160] if last else "stopped")
            break
    with open(log, "w") as f:
        out = subprocess.run(["docker", "logs", name], capture_output=True, text=True)
        f.write(out.stdout + out.stderr)
    subprocess.run(["docker", "stop", "-t", "90", name], capture_output=True)
    subprocess.run(["docker", "rm", "-f", name], capture_output=True)
    return result


def meta(zf):
    """A jar's mod ID, version, and every ID it gives (its own, provides, nested)."""
    names = set(zf.namelist())
    ids, version, own = [], "", ""
    if "fabric.mod.json" in names or "quilt.mod.json" in names:
        if "fabric.mod.json" in names:
            m = json.loads(re.sub(r"[\x00-\x1f]", " ", zf.read("fabric.mod.json").decode("utf-8", "replace")))
            own, version = m.get("id", ""), m.get("version", "")
            ids = [own] + list(m.get("provides", []))
            nested = [j.get("file") for j in m.get("jars", [])]
        else:
            q = json.loads(zf.read("quilt.mod.json"))["quilt_loader"]
            own, version = q.get("id", ""), q.get("version", "")
            ids = [own] + [p if isinstance(p, str) else p.get("id") for p in q.get("provides", [])]
            nested = q.get("jars", [])
        for n in nested:
            try:
                import io
                with zipfile.ZipFile(io.BytesIO(zf.read(n))) as inner:
                    _, _, more = meta(inner)
                    ids += more
            except Exception:
                pass
    elif "META-INF/neoforge.mods.toml" in names or "META-INF/mods.toml" in names:
        toml = zf.read("META-INF/neoforge.mods.toml" if "META-INF/neoforge.mods.toml" in names else "META-INF/mods.toml").decode("utf-8", "replace")
        mods = re.findall(r'^\s*modId\s*=\s*["\']([^"\']+)', toml.split("[[dependencies")[0], re.M)
        versions = re.findall(r'^\s*version\s*=\s*["\']([^"\']+)', toml.split("[[dependencies")[0], re.M)
        own, version = (mods[0] if mods else ""), (versions[-1] if versions else "")
        if "${" in version and "META-INF/MANIFEST.MF" in names:
            mf = zf.read("META-INF/MANIFEST.MF").decode("utf-8", "replace")
            hit = re.search(r"Implementation-Version:\s*(\S+)", mf)
            version = hit.group(1) if hit else version
        ids = mods
    else:
        for n in ("paper-plugin.yml", "plugin.yml"):
            if n in names:
                y = zf.read(n).decode("utf-8", "replace")
                hit = re.search(r"^name:\s*['\"]?([^'\"\n]+)", y, re.M)
                ver = re.search(r"^version:\s*['\"]?([^'\"\n]+)", y, re.M)
                own, version = (hit.group(1).strip() if hit else ""), (ver.group(1).strip() if ver else "")
                ids = [own]
                break
    return own, version, [i for i in ids if i]


def inventory(folder):
    """The jars in mods/ or plugins/: {id: (version, file)}, and every ID given."""
    top, given = {}, {}
    for sub in ("mods", "plugins"):
        d = os.path.join(folder, sub)
        if not os.path.isdir(d):
            continue
        for name in sorted(os.listdir(d)):
            if not name.endswith(".jar"):
                continue
            try:
                with zipfile.ZipFile(os.path.join(d, name)) as zf:
                    own, version, ids = meta(zf)
            except Exception:
                continue
            if own:
                top.setdefault(own, []).append((version, name))
            for i in ids:
                given.setdefault(i, name)
    return top, given


def parse(v):
    v = v.split("+")[0]
    core, _, pre = v.partition("-")
    parts = re.findall(r"\d+", core)
    return [int(p) for p in parts], pre


def compare(a, b):
    """-1, 0, 1 for b older, the same, newer than a; None where unsure."""
    if a == b:
        return 0
    pa, pb = parse(a), parse(b)
    if not pa[0] or not pb[0]:
        return None
    if pa[0] != pb[0]:
        n = max(len(pa[0]), len(pb[0]))
        x, y = pa[0] + [0] * (n - len(pa[0])), pb[0] + [0] * (n - len(pb[0]))
        return 1 if y > x else -1
    if pa[1] == pb[1]:
        return 0
    if not pa[1]:
        return -1
    if not pb[1]:
        return 1
    return 1 if pb[1] > pa[1] else -1


def diff(image, panel, panel_given):
    rows, ok = [], True
    for mod_id in sorted(set(image) | set(panel)):
        iv = ", ".join(v for v, _ in image.get(mod_id, []))
        pv = ", ".join(v for v, _ in panel.get(mod_id, []))
        if mod_id in image and mod_id in panel:
            if len(image[mod_id]) > 1 and len(panel[mod_id]) == 1:
                verdict = "same, one copy instead of %d" % len(image[mod_id]) if any(v == panel[mod_id][0][0] for v, _ in image[mod_id]) else "one copy instead of %d" % len(image[mod_id])
            else:
                c = compare(image[mod_id][0][0], panel[mod_id][0][0])
                verdict = {0: "same", 1: "newer", -1: "OLDER", None: "differs"}[c]
                ok = ok and c != -1
        elif mod_id in image:
            if mod_id in panel_given:
                verdict = "not downloaded: in " + panel_given[mod_id]
            else:
                verdict = "MISSING"
                ok = False
        else:
            verdict = "only the panel's"
        rows.append((mod_id, iv or "-", pv or "-", verdict))
    return rows, ok


def run_plan(plan, args, report):
    base = os.path.join(args.work, plan["name"])
    shutil.rmtree(base, ignore_errors=True)
    # The panel works on files of its own, laid out as the panel server's
    # are; what its steps do to them is done to that server's before it
    # starts.
    image_dir, panel_dir, panel_files = os.path.join(base, "image"), os.path.join(base, "panel"), os.path.join(base, "panel-files")
    for d in (image_dir, panel_dir, panel_files):
        os.makedirs(d)
        lay_uploads(plan, d)
    states_file = os.path.join(base, "states.jsonl")
    env = dict(os.environ, AVSYS_COMPARE_PLAN=json.dumps(plan), AVSYS_COMPARE_DIR=os.path.abspath(panel_files), AVSYS_COMPARE_OUT=states_file)
    r = subprocess.run([args.api_test, "-test.run", "^TestCompareWithImage$", "-test.v"], env=env, capture_output=True, text=True)
    if r.returncode != 0:
        report.append(f"### {plan['name']}\n\nThe panel failed: ```{r.stdout[-2000:]}```\n")
        return False
    states = [json.loads(l) for l in open(states_file)]
    first = states[0]
    # The image needs no files the panel put there first.
    image_result = start(f"cmp-{plan['name']}-image", image_dir, plan, first["own"], "required", args.image, os.path.join(base, "image.log"))
    panel_result = start(f"cmp-{plan['name']}-panel", panel_dir, plan, first["projects"], first["dependencies"], args.image, os.path.join(base, "panel-0.log"))
    image_inv, _ = inventory(image_dir)
    panel_inv, panel_given = inventory(panel_dir)
    rows, ok = diff(image_inv, panel_inv, panel_given)
    # The panel's must start; the image's not starting, as when it takes
    # versions its mods' files say will not do, is the panel doing better.
    ok = ok and panel_result.startswith("Done") and not first["unmet"] and first["dependencies"] == "none"
    verdict = "as good or better" if ok else "NOT as good"
    if ok and not image_result.startswith("Done"):
        verdict = "better: the image's own does not start"
    report.append(f"### {plan['name']} ({plan['loader']} {plan['version']}): {verdict}\n")
    report.append(plan.get("comment", "") + "\n")
    report.append(f"- image (`required`, {len(first['own'])} own entries): **{image_result}**")
    report.append(f"- panel (`{first['dependencies']}`, {len(first['projects'])} listed): **{panel_result}**, notices: {len(first['unmet'])}{' ' + '; '.join(first['unmet']) if first['unmet'] else ''}\n")
    report.append("| mod ID | image | panel | |\n| --- | --- | --- | --- |")
    for mod_id, iv, pv, verdict in rows:
        if verdict != "same":
            report.append(f"| {mod_id} | {iv} | {pv} | {verdict} |")
    same = sum(1 for r in rows if r[3] == "same")
    report.append(f"| ({same} more, the same in both) | | | |\n")
    report.append("| step | answer | listed | start |\n| --- | --- | --- | --- |")
    report.append(f"| create | {first['code']} | {len(first['projects'])} | {panel_result} |")
    for s in states[1:]:
        if s["do"].startswith("addDown"):
            # Nothing changed: refused, saying why.
            fine = s["code"] != 200
            ok = ok and fine
            report.append(f"| {s['do']} (Modrinth down) | {s['code']} {s['message']} | {len(s['projects'])} | {'refused, nothing changed' if fine else 'ACCEPTED'} |")
            continue
        step = plan["steps"][s["step"] - 1]
        if step["do"] == "removeFile":
            os.remove(os.path.join(panel_dir, step["path"]))
        result = start(f"cmp-{plan['name']}-panel", panel_dir, plan, s["projects"], s["dependencies"], args.image, os.path.join(base, f"panel-{s['step']}.log"))
        ok = ok and result.startswith("Done") and s["code"] == 200 and not s["unmet"]
        report.append(f"| {s['do']} | {s['code']} | {len(s['projects'])} | {result}{' (notices: ' + '; '.join(s['unmet']) + ')' if s['unmet'] else ''} |")
    report.append("")
    return ok


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--plans", required=True)
    ap.add_argument("--api-test", required=True)
    ap.add_argument("--work", required=True)
    ap.add_argument("--image", default="itzg/minecraft-server:java21")
    ap.add_argument("sets", nargs="*")
    args = ap.parse_args()
    plans = json.load(open(args.plans))
    report, all_ok = ["## Dependencies: the panel against the image\n"], True
    for plan in plans:
        if args.sets and plan["name"] not in args.sets:
            continue
        print(f"== {plan['name']}", file=sys.stderr)
        all_ok = run_plan(plan, args, report) and all_ok
    print("\n".join(report))
    sys.exit(0 if all_ok else 1)


if __name__ == "__main__":
    main()
