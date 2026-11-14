#!/usr/bin/env python3
"""Backward-compatibility check for contracts/openapi.json and contracts/schemas (API.md §4).

Compares the working tree against a base revision and fails on changes that break existing
clients: removed operations, success responses or response fields, new required request input,
narrowed request types, enums or limits. Response enum additions are reported as warnings.

A deliberate break must be listed in contracts/compat-exceptions.json with a reason and the
PR/ADR that approved the compatibility window. Exceptions only silence findings they match exactly.

Usage: check_contract_compat.py [--base REF]. The base defaults to $CONTRACT_BASE_REF, then to
`git merge-base HEAD origin/dev`. If the base revision has no contract, the check is skipped.
"""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
OPENAPI = 'contracts/openapi.json'
SCHEMA_DIR = 'contracts/schemas'
EXCEPTIONS = ROOT / 'contracts/compat-exceptions.json'
METHODS = ('get', 'put', 'post', 'delete', 'patch', 'head', 'options', 'trace')
# Request-side limits: a larger lower bound or smaller upper bound rejects previously valid input.
LOWER = ('minLength', 'minItems', 'minimum', 'exclusiveMinimum', 'minProperties')
UPPER = ('maxLength', 'maxItems', 'maximum', 'exclusiveMaximum', 'maxProperties')


def types(s):
    t = s.get('type')
    if t is None:
        return None
    return set(t) if isinstance(t, list) else {t}


def narrowed(old_t, new_t):
    """True when new_t accepts fewer JSON types than old_t (None = any type)."""
    if new_t is None:
        return False
    if old_t is None:
        return True
    widened = new_t | ({'integer'} if 'number' in new_t else set())
    return not old_t <= widened


class Diff:
    def __init__(self, old_doc, new_doc):
        self.old_doc = old_doc
        self.new_doc = new_doc
        self.breaking: list[str] = []
        self.warnings: list[str] = []
        self.seen: set = set()

    def resolve(self, doc, node):
        hops = 0
        while isinstance(node, dict) and '$ref' in node:
            ref = node['$ref']
            if not ref.startswith('#/'):
                raise SystemExit(f'unsupported external $ref {ref}')
            target = doc
            for part in ref[2:].split('/'):
                target = target[part.replace('~1', '/').replace('~0', '~')]
            node = target
            hops += 1
            if hops > 32:
                raise SystemExit(f'$ref loop at {ref}')
        return node if isinstance(node, dict) else {}

    def br(self, loc, msg):
        self.breaking.append(f'{loc}: {msg}')

    # ---- schemas ----------------------------------------------------------------------
    def schema(self, old, new, direction, loc):
        old = self.resolve(self.old_doc, old)
        new = self.resolve(self.new_doc, new)
        key = (id(old), id(new), direction)
        if key in self.seen:
            return
        self.seen.add(key)
        req = direction == 'request'
        self.compare_type(old, new, req, loc)
        self.compare_enum(old, new, req, loc)
        if req:
            self.compare_limits(old, new, loc)
        self.compare_object(old, new, req, direction, loc)
        if 'items' in old and 'items' in new:
            self.schema(old['items'], new['items'], direction, f'{loc}[]')
        self.compare_combinators(old, new, req, direction, loc)

    def compare_type(self, old, new, req, loc):
        ot, nt = types(old), types(new)
        if req and narrowed(ot, nt):
            self.br(loc, f'request type narrowed {sorted(ot or ["any"])} -> {sorted(nt)}')
        if not req and narrowed(nt, ot):
            self.br(loc, f'response type widened {sorted(ot)} -> {sorted(nt or ["any"])}')
        if ('const' in old or 'const' in new) and old.get('const') != new.get('const'):
            if req or 'const' in old:
                self.br(loc, f'const changed {old.get("const")!r} -> {new.get("const")!r}')

    def compare_enum(self, old, new, req, loc):
        oe, ne = old.get('enum'), new.get('enum')
        if req and ne is not None:
            lost = ['<any value>'] if oe is None else [v for v in oe if v not in ne]
            if lost:
                self.br(loc, f'request enum no longer accepts {lost}')
        if not req and oe is not None:
            added = ['<any value>'] if ne is None else [v for v in ne if v not in oe]
            if added:
                self.warnings.append(f'{loc}: response enum gained {added}; clients must tolerate unknown values')

    def compare_limits(self, old, new, loc):
        for k in LOWER:
            if k in new and (k not in old or new[k] > old[k]):
                self.br(loc, f'request {k} tightened {old.get(k)} -> {new[k]}')
        for k in UPPER:
            if k in new and (k not in old or new[k] < old[k]):
                self.br(loc, f'request {k} tightened {old.get(k)} -> {new[k]}')
        for k in ('pattern', 'format'):
            if k in new and new.get(k) != old.get(k):
                self.br(loc, f'request {k} changed {old.get(k)!r} -> {new[k]!r}')

    def compare_object(self, old, new, req, direction, loc):
        oprops = old.get('properties', dict())
        nprops = new.get('properties', dict())
        oreq = set(old.get('required', []))
        nreq = set(new.get('required', []))
        closed = new.get('additionalProperties') is False
        if req:
            for name in sorted(nreq - oreq):
                self.br(loc, f'request field {name} became required')
            if closed and old.get('additionalProperties') is not False:
                self.br(loc, 'request object closed (additionalProperties: false)')
            if closed:
                for name in sorted(set(oprops) - set(nprops)):
                    self.br(loc, f'request field {name} removed from closed object')
        else:
            for name in sorted(set(oprops) - set(nprops)):
                self.br(loc, f'response field {name} removed')
            for name in sorted((oreq - nreq) & set(nprops)):
                self.br(loc, f'response field {name} no longer required')
        for name in sorted(set(oprops) & set(nprops)):
            self.schema(oprops[name], nprops[name], direction, f'{loc}.{name}')
        oa, na = old.get('additionalProperties'), new.get('additionalProperties')
        if isinstance(oa, dict) and isinstance(na, dict):
            self.schema(oa, na, direction, f'{loc}{{*}}')

    def compare_combinators(self, old, new, req, direction, loc):
        for comb in ('anyOf', 'oneOf'):
            oc, nc = old.get(comb), new.get(comb)
            if oc is None and nc is None:
                continue
            if oc is None or nc is None:
                self.br(loc, f'{comb} added or removed')
            elif len(oc) == len(nc):
                for i, (a, b) in enumerate(zip(oc, nc)):
                    self.schema(a, b, direction, f'{loc}<{comb}{i}>')
            elif req and len(nc) < len(oc):
                self.br(loc, f'request {comb} lost alternatives {len(oc)} -> {len(nc)}')
            elif not req and len(nc) > len(oc):
                self.br(loc, f'response {comb} gained alternatives {len(oc)} -> {len(nc)}')

    # ---- OpenAPI operations ----------------------------------------------------------
    def openapi(self):
        opaths = self.old_doc.get('paths', {})
        npaths = self.new_doc.get('paths', {})
        for path, oitem in opaths.items():
            nitem = npaths.get(path)
            for method in METHODS:
                if method not in oitem:
                    continue
                loc = f'{method.upper()} {path}'
                if nitem is None or method not in nitem:
                    self.br(loc, 'operation removed')
                    continue
                self.operation(oitem, nitem, oitem[method], nitem[method], loc)

    @staticmethod
    def params(doc_resolve, item, op):
        out = dict()
        for p in item.get('parameters', []) + op.get('parameters', []):
            p = doc_resolve(p)
            out[(p.get('in'), p.get('name'))] = p
        return out

    def operation(self, oitem, nitem, oop, nop, loc):
        oparams = self.params(lambda p: self.resolve(self.old_doc, p), oitem, oop)
        nparams = self.params(lambda p: self.resolve(self.new_doc, p), nitem, nop)
        for key, p in nparams.items():
            if p.get('required') and not oparams.get(key, dict()).get('required'):
                self.br(loc, f'{key[0]} parameter {key[1]} is now required')
        for key, p in oparams.items():
            if key in nparams:
                self.schema(p.get('schema', {}), nparams[key].get('schema', {}), 'request', f'{loc} {key[0]}:{key[1]}')
        self.request_body(oop, nop, loc)
        self.responses(oop, nop, loc)

    def request_body(self, oop, nop, loc):
        ob = self.resolve(self.old_doc, oop.get('requestBody', {}))
        nb = self.resolve(self.new_doc, nop.get('requestBody', {}))
        if nb.get('required') and not ob.get('required'):
            self.br(loc, 'request body became required')
        for ctype, media in ob.get('content', {}).items():
            nmedia = nb.get('content', {}).get(ctype)
            if nmedia is None:
                if nb:
                    self.br(loc, f'request content type {ctype} no longer accepted')
                continue
            self.schema(media.get('schema', {}), nmedia.get('schema', {}), 'request', f'{loc} body')

    def responses(self, oop, nop, loc):
        nres = nop.get('responses', {})
        for code, oresp in oop.get('responses', {}).items():
            if code not in nres:
                if str(code).startswith('2'):
                    self.br(loc, f'success response {code} removed')
                continue
            oresp = self.resolve(self.old_doc, oresp)
            nresp = self.resolve(self.new_doc, nres[code])
            for ctype, media in oresp.get('content', {}).items():
                nmedia = nresp.get('content', dict()).get(ctype)
                if nmedia is None:
                    self.br(loc, f'response {code} content type {ctype} removed')
                    continue
                self.schema(media.get('schema', dict()), nmedia.get('schema', dict()), 'response', f'{loc} {code}')


def compare(old_api, new_api, old_schemas, new_schemas):
    d = Diff(old_api, new_api)
    d.openapi()
    for name, old in sorted(old_schemas.items()):
        loc = f'{SCHEMA_DIR}/{name}'
        new = new_schemas.get(name)
        if new is None:
            d.br(loc, 'request schema removed')
            continue
        sd = Diff(old, new)
        sd.schema(old, new, 'request', loc)
        d.breaking += sd.breaking
        d.warnings += sd.warnings
    return d


def git(*args):
    return subprocess.run(['git', *args], cwd=ROOT, capture_output=True, text=True)


def default_base():
    if os.environ.get('CONTRACT_BASE_REF'):
        return os.environ['CONTRACT_BASE_REF']
    r = git('merge-base', 'HEAD', 'origin/dev')
    if r.returncode != 0:
        raise SystemExit('cannot determine base: pass --base or set CONTRACT_BASE_REF (is origin/dev fetched?)')
    return r.stdout.strip()


def load_base(base):
    r = git('show', f'{base}:{OPENAPI}')
    if r.returncode != 0:
        return None, None
    names = git('ls-tree', '--name-only', f'{base}:{SCHEMA_DIR}').stdout.split()
    schemas = {}
    for n in names:
        if n.endswith('.schema.json'):
            schemas[n] = json.loads(git('show', f'{base}:{SCHEMA_DIR}/{n}').stdout)
    return json.loads(r.stdout), schemas


def load_exceptions(path=EXCEPTIONS):
    if not path.exists():
        return dict()
    out = dict()
    for e in json.loads(path.read_text()):
        if not (isinstance(e, dict) and e.get('finding') and e.get('reason') and e.get('approved_in')):
            raise SystemExit(f'{path.name}: every entry needs finding, reason and approved_in')
        out[e['finding']] = e
    return out


def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument('--base')
    args = ap.parse_args(argv)
    base = args.base or default_base()
    old_api, old_schemas = load_base(base)
    if old_api is None:
        print(f'contract compat: {OPENAPI} absent at {base}; skipped')
        return 0
    new_api = json.loads((ROOT / OPENAPI).read_text())
    new_schemas = {p.name: json.loads(p.read_text()) for p in (ROOT / SCHEMA_DIR).glob('*.schema.json')}
    d = compare(old_api, new_api, old_schemas, new_schemas)
    exceptions = load_exceptions()
    for f in d.warnings:
        print(f'warning: {f}')
    failures = []
    for f in d.breaking:
        if f in exceptions:
            print(f'accepted: {f} ({exceptions[f]["approved_in"]})')
        else:
            failures.append(f)
            print(f'BREAKING: {f}', file=sys.stderr)
    if failures:
        print(f'contract compat: {len(failures)} breaking change(s) against {base[:12]}; keep a compatibility '
            f'window or record an approved exception in {EXCEPTIONS.relative_to(ROOT)}', file=sys.stderr)
        return 1
    print(f'contract compat: OK against {base[:12]} ({len(d.warnings)} warning(s))')
    return 0


if __name__ == '__main__':
    sys.exit(main())
