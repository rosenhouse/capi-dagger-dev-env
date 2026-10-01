#!/usr/bin/env python3
"""Turns the restore spike's samples into Markdown tables, and results.json: report.py <out-dir>.

Each phase in phases.log is a window about one machine. Host figures come from
host-samples.log (sampler-host.sh), guest figures from guest-<machine>.log (sampler-guest.sh).
"""
import json
import re
import sys
from collections import defaultdict
from pathlib import Path

OUT = Path(sys.argv[1])
BUCKET = 15
SERIES_SPAN = 300

VMX_EXITS = {0: "EXCEPTION_NMI", 1: "EXTERNAL_INTERRUPT", 7: "INTERRUPT_WINDOW", 10: "CPUID", 12: "HLT",
             18: "VMCALL", 28: "CR_ACCESS", 30: "IO_INSTRUCTION", 31: "MSR_READ", 32: "MSR_WRITE", 40: "PAUSE",
             44: "APIC_ACCESS", 45: "EOI_INDUCED", 48: "EPT_VIOLATION", 49: "EPT_MISCONFIG", 52: "PREEMPTION_TIMER",
             54: "WBINVD", 55: "XSETBV", 56: "APIC_WRITE", 58: "INVPCID"}
SVM_EXITS = {0x60: "INTR", 0x61: "NMI", 0x63: "INIT", 0x64: "VINTR", 0x6e: "RDTSC", 0x72: "CPUID", 0x77: "PAUSE",
             0x78: "HLT", 0x79: "INVLPG", 0x7b: "IOIO", 0x7c: "MSR", 0x81: "VMMCALL", 0x89: "WBINVD",
             0x8d: "XSETBV", 0x400: "NPF", 0x401: "AVIC_INCOMPLETE_IPI", 0x402: "AVIC_UNACCELERATED_ACCESS"}


def exit_name(code, amd):
    if not amd:
        return VMX_EXITS.get(code, f"vmx{code}")
    if code in SVM_EXITS:
        return SVM_EXITS[code]
    if 0x40 <= code < 0x60:
        return f"EXCP{code - 0x40}"
    if code < 0x20:
        return f"{'READ' if code < 0x10 else 'WRITE'}_CR{code % 16}"
    if 0x90 <= code < 0xa0:
        return f"CR{code - 0x90}_WRITE_TRAP"
    return f"svm{code:#x}"


def fault_name(code):
    bits = [n for b, n in ((1, "present"), (2, "write"), (4, "user"), (16, "fetch")) if code & b]
    return "+".join(bits) or "not-present-read"


MADVISE = {4: "DONTNEED", 8: "FREE", 9: "REMOVE", 14: "HUGEPAGE", 15: "NOHUGEPAGE", 22: "POPULATE_READ",
           23: "POPULATE_WRITE", 25: "COLLAPSE"}


def madvise_name(key):
    comm, _, behavior = key.rpartition("/")
    return f"{comm} {MADVISE.get(int(behavior), behavior)}"


def number(text):
    try:
        return float(text)
    except ValueError:
        return None


def read_host():
    samples, cur = [], None
    path = OUT / "host-samples.log"
    for line in (path.read_text(errors="replace") if path.exists() else "").splitlines():
        kind, _, rest = line.partition(" ")
        if kind == "T":
            cur = {"t": float(rest), "mem": {}, "vm": {}, "proc": {}, "kvm": {}, "hist": defaultdict(dict)}
            samples.append(cur)
        elif cur is None:
            continue
        elif kind == "M":
            for tok in rest.split():
                k, _, v = tok.partition(":")
                cur["mem"][k] = number(v)
        elif kind == "V":
            for tok in rest.split():
                k, _, v = tok.partition("=")
                cur["vm"][k] = number(v)
        elif kind == "P":
            f = rest.split()
            # f[0] is the pid; f[1:] are /proc/<pid>/stat fields from state (field 3) on.
            st = f[1:]
            cur["proc"][f[0]] = {"minflt": int(st[7]), "majflt": int(st[9]), "utime": int(st[11]),
                                 "stime": int(st[12]), "guest": int(st[40]),
                                 **{t.split(":")[0]: int(t.split(":")[1]) for t in f if ":" in t}}
        elif kind == "K":
            pid, _, kv = rest.partition(" ")
            cur["kvm"][pid] = {k: number(v) for k, _, v in (t.partition("=") for t in kv.split())}
        elif kind == "H":
            event, _, body = rest.partition(" ")
            m = re.match(r"\{\s*(.*?)\s*\}\s*hitcount:\s*(\d+)(?:\s+len_in:\s*(\d+))?", body)
            if m:
                # "{ common_pid: libkrun VM [ 1234], behavior: 4 }" becomes "libkrun VM/4".
                parts = re.split(r",\s*(?=\w+:)", m.group(1))
                key = "/".join(re.sub(r"\s*\[\s*\d+\]$", "", p.split(":", 1)[1].strip()) for p in parts)
                cur["hist"][event][key] = int(m.group(3) if m.group(3) else m.group(2))
    return samples


def read_guest(path):
    samples, names, cur = [], {}, None
    for line in path.read_text(errors="replace").splitlines():
        kind, _, rest = line.partition(" ")
        f = rest.split()
        if kind == "D" and len(f) == 2:
            names[f[0]] = f[1]
        elif kind == "T" and len(f) >= 2:
            cur = {"up": float(f[0]), "rt": int(f[1])}
            samples.append(cur)
        elif cur is None:
            continue
        elif kind == "S":
            cur["cpu"] = [int(x) for x in f[1:9]]
            cur.update({f[i]: int(f[i + 1]) for i in range(11, len(f) - 1, 2)})
        elif kind == "V":
            cur["vm"] = {k: int(v) for k, _, v in (t.partition("=") for t in f)}
        elif kind == "I":
            cur["irq"] = {k: int(v) for k, _, v in (t.partition(":") for t in f)}
        elif kind == "R":
            cur["readyz"] = f
        elif kind == "Q":
            procs = []
            for tok in f:
                ticks, _, who = tok.partition(":")
                comm, _, cid = who.rpartition("@")
                procs.append((int(ticks), f"{comm}@{names.get(cid, cid)}"))
            cur["top"] = procs
    return samples


def load_lines(name):
    path = OUT / name
    return [l.split() for l in path.read_text().splitlines() if l.strip()] if path.exists() else []


def window(samples, key, start, end):
    return [s for s in samples if start <= s[key] <= end]


def host_stats(win, pid, amd):
    if len(win) < 2:
        return {}
    a, b = win[0], win[-1]
    dt = b["t"] - a["t"]
    out = {"host_s": dt}
    pa, pb = a["proc"].get(pid), b["proc"].get(pid)
    if pa and pb:
        guest = (pb["guest"] - pa["guest"]) / 100
        out.update(vmm_guest_cores=guest / dt, vmm_kernel_cores=(pb["stime"] - pa["stime"]) / 100 / dt,
                   vmm_user_cores=((pb["utime"] - pa["utime"]) / 100 - guest) / dt,
                   vmm_minflt_s=(pb["minflt"] - pa["minflt"]) / dt, vmm_rss_anon_mib=pb.get("RssAnon", 0) / 1024,
                   vmm_rss_file_mib=pb.get("RssFile", 0) / 1024, vmm_rss_shmem_mib=pb.get("RssShmem", 0) / 1024)
    ka, kb = a["kvm"].get(pid), b["kvm"].get(pid)
    if ka and kb:
        for k in ("exits", "pf_taken", "pf_fixed", "pf_fast", "pf_spurious", "pf_mmio_spte_created", "mmio_exits",
                  "io_exits", "halt_exits", "irq_exits", "remote_tlb_flush", "tlb_flush", "insn_emulation",
                  "hypercalls", "request_irq_exits", "signal_exits"):
            if kb.get(k) is not None and ka.get(k) is not None:
                out[f"kvm_{k}_s"] = (kb[k] - ka[k]) / dt
        for k in ("pages_4k", "pages_2m", "pages_1g"):
            if kb.get(k) is not None:
                out[f"kvm_{k}"] = kb[k]
    for k in ("pgfault", "pgmajfault", "thp_fault_alloc", "thp_fault_fallback", "thp_split_pmd", "thp_collapse_alloc",
              "compact_stall", "compact_migrate_scanned", "pgmigrate_success", "pgsteal_kswapd", "pgsteal_direct",
              "nr_dirty", "nr_writeback", "pgpgout", "oom_kill"):
        if b["vm"].get(k) is not None and a["vm"].get(k) is not None:
            out[f"host_{k}"] = b["vm"][k] if k.startswith("nr_") else b["vm"][k] - a["vm"][k]
    out["host_min_available_mib"] = min(s["mem"].get("MemAvailable", 0) for s in win) / 1024
    hists = [s for s in win if s["hist"]]
    if len(hists) >= 2:
        ha, hb = hists[0]["hist"], hists[-1]["hist"]
        hdt = hists[-1]["t"] - hists[0]["t"]
        for event, label, name, scale in (
                ("kvm/kvm_exit", "exits", lambda k: exit_name(int(k), amd), 1),
                ("kvm/kvm_page_fault", "faults", lambda k: fault_name(int(k, 0)), 1),
                ("kvmmmu/kvm_mmu_spte_requested", "spte_requested_level", str, 1),
                ("kvm/kvm_unmap_hva_range", "unmap_hva_range", str, 1),
                ("syscalls/sys_enter_madvise", "madvise_mib", madvise_name, 2 ** -20)):
            rates = defaultdict(float)
            for k, v in hb.get(event, {}).items():
                try:
                    n = name(k)
                except ValueError:
                    n = k
                rates[n] += (v - ha.get(event, {}).get(k, 0)) * scale / hdt
            out[label] = dict(sorted(((k, round(v, 1)) for k, v in rates.items() if v > 0), key=lambda kv: -kv[1]))
    return out


def guest_stats(win):
    win = [s for s in win if "cpu" in s]
    if len(win) < 2:
        return {}
    a, b = win[0], win[-1]
    d = [y - x for x, y in zip(a["cpu"], b["cpu"])]
    total = sum(d) or 1
    dt = b["up"] - a["up"] or 1
    out = {"guest_s": dt, "guest_user_pct": 100 * (d[0] + d[1]) / total, "guest_sys_pct": 100 * d[2] / total,
           "guest_idle_pct": 100 * d[3] / total, "guest_iowait_pct": 100 * d[4] / total,
           "guest_irq_pct": 100 * (d[5] + d[6]) / total, "guest_steal_pct": 100 * d[7] / total,
           "guest_ctxt_s": (b.get("ctxt", 0) - a.get("ctxt", 0)) / dt,
           "guest_intr_s": (b.get("intr", 0) - a.get("intr", 0)) / dt}
    for k in ("LOC", "RES", "CAL", "TLB", "DEV"):
        if k in a.get("irq", {}) and k in b.get("irq", {}):
            out[f"guest_{k}_s"] = (b["irq"][k] - a["irq"][k]) / dt
    for k in ("pgfault", "pgmajfault", "pgfree", "pgscan_kswapd", "compact_stall", "thp_fault_alloc"):
        if k in a.get("vm", {}) and k in b.get("vm", {}):
            out[f"guest_{k}_s"] = (b["vm"][k] - a["vm"][k]) / dt
    probes = [s["readyz"] for s in win if "readyz" in s]
    if probes:
        out["readyz_ok_mgmt_pct"] = 100 * sum(p[0] == "200" for p in probes) / len(probes)
        out["readyz_ok_work_pct"] = 100 * sum(len(p) > 1 and p[1] == "200" for p in probes) / len(probes)
    top = defaultdict(int)
    for s in win[1:]:
        for ticks, who in s.get("top", []):
            top[who] += ticks
    out["top_cpu_s"] = {k: round(v / 100, 1) for k, v in sorted(top.items(), key=lambda kv: -kv[1])[:8]}
    return out


def fmt(v, digits=0):
    return "" if v is None else f"{v:.{digits}f}"


def main():
    amd = "AuthenticAMD" in Path("/proc/cpuinfo").read_text()
    host = read_host()
    guests = {p.stem[len("guest-"):]: read_guest(p) for p in OUT.glob("guest-*.log") if p.stem.count("-") == 1}
    phases = load_lines("phases.log")
    pids = load_lines("pids.log")
    end_of_run = host[-1]["t"] if host else 0
    results = []
    for i, (start, name, machine) in enumerate(phases):
        start = float(start)
        end = float(phases[i + 1][0]) if i + 1 < len(phases) else end_of_run
        pid = next((p for t, m, p in reversed(pids) if m == machine and float(t) <= end), None)
        hw = window(host, "t", start, end)
        gw = window(guests.get(machine, []), "rt", int(start), int(end) + 1)
        row = {"phase": name, "machine": machine, "start": start, "seconds": end - start,
               **host_stats(hw, pid, amd), **guest_stats(gw)}
        series = []
        for b0 in range(0, int(min(end - start, SERIES_SPAN)), BUCKET):
            bh = window(host, "t", start + b0 - 2.5, start + b0 + BUCKET + 2.5)
            bg = window(guests.get(machine, []), "rt", int(start) + b0 - 3, int(start) + b0 + BUCKET + 3)
            series.append({"t": b0, **host_stats(bh, pid, amd), **guest_stats(bg),
                           "readyz": (bg[-1].get("readyz") if bg else None)})
        row["series"] = series
        results.append(row)
    (OUT / "results.json").write_text(json.dumps(results, indent=1))

    print("### Phases\n")
    print("VMM cores split CPU time of the machine's VMM into guest mode, host kernel (KVM exits, page faults)"
          " and VMM user space. Exits and faults are KVM tracepoint counts for all VMs on the host.\n")
    print("| phase | machine | s | guest user, sys, idle % | VMM cores guest, kernel, user | VMM minflt/s |"
          " KVM exits/s | top exits/s | KVM faults/s | SPTE levels requested/s | madvise MiB/s |"
          " guest ctxt/s, TLB IPI/s | readyz ok mgmt, work % | busiest guest processes, CPU s |")
    print("|" + "---|" * 14)
    for r in results:
        print(f"| {r['phase']} | {r['machine']} | {fmt(r['seconds'])} "
              f"| {fmt(r.get('guest_user_pct'))}, {fmt(r.get('guest_sys_pct'))}, {fmt(r.get('guest_idle_pct'))} "
              f"| {fmt(r.get('vmm_guest_cores'), 2)}, {fmt(r.get('vmm_kernel_cores'), 2)}, {fmt(r.get('vmm_user_cores'), 2)} "
              f"| {fmt(r.get('vmm_minflt_s'))} | {fmt(r.get('kvm_exits_s'))} "
              f"| {', '.join(f'{k} {v:.0f}' for k, v in list(r.get('exits', {}).items())[:4])} "
              f"| {', '.join(f'{k} {v:.0f}' for k, v in list(r.get('faults', {}).items())[:3])} "
              f"| {', '.join(f'L{k} {v:.0f}' for k, v in r.get('spte_requested_level', {}).items())} "
              f"| {', '.join(f'{k} {v:.0f}' for k, v in list(r.get('madvise_mib', {}).items())[:2])} "
              f"| {fmt(r.get('guest_ctxt_s'))}, {fmt(r.get('guest_TLB_s'))} "
              f"| {fmt(r.get('readyz_ok_mgmt_pct'))}, {fmt(r.get('readyz_ok_work_pct'))} "
              f"| {', '.join(f'{k} {v}' for k, v in list(r.get('top_cpu_s', {}).items())[:5])} |")
    for r in results:
        if not r["series"] or r["phase"] in ("capture", "zero-fill") or r["phase"].startswith("create-"):
            continue
        print(f"\n### {r['phase']} ({r['machine']}), {BUCKET} s buckets\n")
        print("| t s | guest idle, sys % | VMM cores guest, kernel | VMM minflt/s | KVM exits/s | NPF/EPT exits/s |"
              " KVM faults/s | guest pgfault/s | readyz mgmt, work |")
        print("|" + "---|" * 9)
        for s in r["series"]:
            ex = s.get("exits", {})
            tdp = ex.get("NPF", ex.get("EPT_VIOLATION"))
            print(f"| {s['t']} | {fmt(s.get('guest_idle_pct'))}, {fmt(s.get('guest_sys_pct'))} "
                  f"| {fmt(s.get('vmm_guest_cores'), 2)}, {fmt(s.get('vmm_kernel_cores'), 2)} "
                  f"| {fmt(s.get('vmm_minflt_s'))} | {fmt(s.get('kvm_exits_s'))} | {fmt(tdp)} "
                  f"| {fmt(sum(s.get('faults', {}).values()) if s.get('faults') else None)} "
                  f"| {fmt(s.get('guest_pgfault_s'))} | {' '.join(s.get('readyz') or [])} |")


if __name__ == "__main__":
    main()
