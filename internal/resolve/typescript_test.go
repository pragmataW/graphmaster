package resolve

import (
	"strconv"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

// tsCase is one expected reference: from src, the reference named name
// resolves to dst (path::Chain, "" = unresolved) with conf.
type tsCase struct {
	src, name, dst string
	conf           int
}

func checkTSCases(t *testing.T, r *testRepo, cases []tsCase) {
	t.Helper()
	es := r.edges()
	for _, c := range cases {
		got := edgesFrom(es, c.src, c.name)
		if len(got) == 0 {
			t.Errorf("%s -> %s: no edge%s", c.src, c.name, describeFrom(es, c.src))
			continue
		}
		for _, e := range got {
			if e.Dst != c.dst || e.Conf != c.conf {
				t.Errorf("%s -> %s (line %d): got %q conf %d, want %q conf %d", c.src, c.name, e.Line, e.Dst, e.Conf, c.dst, c.conf)
			}
		}
	}
}

func TestTSResolverRegistered(t *testing.T) {
	for _, l := range []model.Lang{model.LangTypeScript, model.LangTSX, model.LangJavaScript} {
		if _, ok := For(l).(tsResolver); !ok {
			t.Errorf("%v: resolver %T", l, For(l))
		}
	}
	r := tsResolver{}
	if got := r.ImportBindings(Import{Spec: "./x", Names: []string{"a"}}); got != nil {
		t.Errorf("named import binds %v", got)
	}
	if got := r.ImportBindings(Import{Spec: "./x", Alias: "ns"}); len(got) != 1 || got[0] != "ns" {
		t.Errorf("namespace import binds %v", got)
	}
}

func TestTSImportsAndQualifiers(t *testing.T) {
	r := fullBuild(t, map[string]string{
		"tsconfig.json": `{"compilerOptions": {"baseUrl": ".", "paths": {"~/*": ["src/*"]}}}`,
		"src/models/user.ts": `export class User { greet(): string { return "hi"; } static create(): User { return new User(); } }
export enum Kind { A, B }
export function helper(): number { return 1; }
export namespace Geo { export function area(): number { return 0; } }
`,
		"src/models/index.ts": `export * from "./user";
export { helper as aid } from "./user";
`,
		"src/util.js": `exports.parse = function parse(s) { return s; };
function local() { const inner = () => 1; return inner(); }
module.exports.local = local;
`,
		"src/app.ts": `import { User, Kind } from "./models";
import * as models from "~/models/user";
import { helper as h } from "~/models/user";
import Def from "./def";
const util = require("./util");
import React from "react";

export function run(u: User): void {
  u.greet();
  models.helper();
  models.Geo.area();
  h();
  util.parse("x");
  User.create();
  const k = Kind.A;
  React.createElement("div");
  console.log(k);
  inner();
  Def.make();
}
`,
		"src/def.ts": `export default class Def { static make(): Def { return new Def(); } }
`,
	}, nil)
	const app = "src/app.ts::run"
	checkTSCases(t, r, []tsCase{
		{app, "greet", "src/models/user.ts::User.greet", 80},
		{app, "helper", "src/models/user.ts::helper", 100}, // namespace import and aliased h()
		{app, "area", "src/models/user.ts::Geo.area", 100},
		{app, "parse", "src/util.js::parse", 100}, // require alias
		{app, "create", "src/models/user.ts::User.create", 80},
		{app, "A", "src/models/user.ts::Kind.A", 80},
		{app, "createElement", "", 0},
		{app, "log", "", 0},
		{app, "inner", "", 0}, // a local helper of another file
		{app, "make", "src/def.ts::Def.make", 80},
	})
}

func TestTSReceiverTyping(t *testing.T) {
	r := fullBuild(t, map[string]string{
		"src/svc.ts": `
export interface Store { get(id: string): Item | undefined; }
export class Item { price(): number { return 1; } owner?: Owner; }
export class Owner { notify(): void {} }
export type Maybe = Item | null;
export type Alias = Store;

export class Base { save(): void {} }
export class Repo extends Base implements Store {
  private cache: Map<string, Item> = new Map();
  constructor(private readonly store: Store, public label: string) { super(); }
  get(id: string): Item | undefined { return this.cache.get(id); }
  async load(id: string): Promise<Item> { return this.get(id)!; }
  run(alias: Alias, maybe: Maybe, raw: unknown): void {
    this.store.get("a");
    this.get("a")?.price();
    this.get("a")!.owner!.notify();
    alias.get("b");
    maybe?.price();
    (raw as Item).price();
    super.save();
    this.label.toUpperCase();
    const local = new Item();
    local.price();
    const fn = function () { this.price(); };
  }
  async later(): Promise<void> {
    const it = await this.load("x");
    it.price();
  }
}

export const api = {
  ping() { return 1; },
  pong: () => 2,
};

export function useApi(): void {
  api.ping();
  api.pong();
  ping();
}
`,
	}, nil)
	const repo = "src/svc.ts::Repo."
	checkTSCases(t, r, []tsCase{
		{repo + "get", "get", "", 0}, // Map.get
		{repo + "run", "price", "src/svc.ts::Item.price", 80},
		{repo + "run", "notify", "src/svc.ts::Owner.notify", 80},
		{repo + "run", "owner", "src/svc.ts::Item.owner", 80},
		{repo + "run", "save", "src/svc.ts::Base.save", 80},
		{repo + "run", "toUpperCase", "", 0},
		{repo + "run", "store", "src/svc.ts::Repo.store", 80},
		{repo + "run", "Item", "src/svc.ts::Item", 90},
		{repo + "later", "price", "src/svc.ts::Item.price", 80},
		{repo + "later", "load", "src/svc.ts::Repo.load", 80},
		{"src/svc.ts::useApi", "pong", "src/svc.ts::api.pong", 80},
	})
	// this.store.get / alias.get: the Store interface method (receiver typed
	// through a field, a parameter property and a type alias).
	es := r.edges()
	for _, e := range edgesFrom(es, repo+"run", "get") {
		if e.Dst != "src/svc.ts::Store.get" && e.Dst != repo+"get" {
			t.Errorf("get -> %q conf %d", e.Dst, e.Conf)
		}
		if e.Conf != 80 {
			t.Errorf("get conf %d", e.Conf)
		}
	}
	// api.ping() is typed through the object literal; a bare ping() must
	// not reach the member.
	pings := map[string]int{}
	for _, e := range edgesFrom(es, "src/svc.ts::useApi", "ping") {
		pings[e.Dst] = e.Conf
	}
	if len(pings) != 2 || pings["src/svc.ts::api.ping"] != 80 || pings[""] != 0 {
		t.Errorf("ping edges = %v", pings)
	}
}

func TestTSXComponentsAndJSXMembers(t *testing.T) {
	r := fullBuild(t, map[string]string{
		"ui/Button.tsx": `export function Button(props: { label: string }) { return <button>{props.label}</button>; }
export const Icons = { Gear: () => <i /> };
`,
		"ui/Toolbar.tsx": `import { Button, Icons } from "./Button";
import * as Lib from "./lib";
export function Toolbar() {
  return (<div><Button label="x" /><Icons.Gear /><Lib.Panel /><Local /></div>);
}
function Local() { return <span />; }
`,
		"ui/lib.tsx": `export function Panel() { return null; }
`,
	}, nil)
	const tb = "ui/Toolbar.tsx::Toolbar"
	checkTSCases(t, r, []tsCase{
		{tb, "Button", "ui/Button.tsx::Button", 100},
		{tb, "Gear", "ui/Button.tsx::Icons.Gear", 80},
		{tb, "Panel", "ui/lib.tsx::Panel", 100},
		{tb, "Local", "ui/Toolbar.tsx::Local", 90},
	})
}

func TestTSTypeResolution(t *testing.T) {
	r := fullBuild(t, map[string]string{
		"a/types.ts": `export interface Shape { area(): number }
export type Id = string;
`,
		"a/index.ts": `export { Shape } from "./types";
`,
		"b/use.ts": `import { Shape } from "../a";
import type { Id } from "../a/types";
export class Sq implements Shape { id: Id = ""; area(): number { return 1; } }
`,
		// Calls and plain references through a barrel are resolved by the
		// framework's T1 rule, which does not follow re-exports; import
		// directly here. Type relations (implements above) do follow them.
		"b/direct.ts": `import { Shape } from "../a/types";
export function f(p: Promise<Shape>, m: Map<string, Shape>, e: HTMLElement): void {}
`,
		"c/global.d.ts": `declare interface GlobalThing { x: number }
`,
		"c/user.ts": `export function g(t: GlobalThing): void {}
`,
	}, nil)
	es := r.edges()
	want := map[string]string{
		"implements|b/use.ts::Sq|Shape":         "a/types.ts::Shape|100",
		"references|b/use.ts::Sq.id|Id":         "a/types.ts::Id|100",
		"references|b/direct.ts::f|Shape":       "a/types.ts::Shape|100",
		"references|b/direct.ts::f|Promise":     "|0",
		"references|b/direct.ts::f|Map":         "|0",
		"references|b/direct.ts::f|HTMLElement": "|0",
		"references|c/user.ts::g|GlobalThing":   "c/global.d.ts::GlobalThing|50",
	}
	seen := map[string]bool{}
	for _, e := range es {
		k := e.Kind.String() + "|" + e.Src + "|" + e.Name
		w, ok := want[k]
		if !ok {
			continue
		}
		seen[k] = true
		if got := e.Dst + "|" + strconv.Itoa(e.Conf); got != w {
			t.Errorf("%s: got %s, want %s", k, got, w)
		}
	}
	for k := range want {
		if !seen[k] {
			t.Errorf("%s: no edge", k)
		}
	}
}

// TestTSMalformedSources indexes files that do not parse cleanly and
// sources that changed after indexing; resolution must not fail.
func TestTSMalformedSources(t *testing.T) {
	files := map[string]string{
		"x/broken.ts": "export class { m( { this.q.( }\nimport {",
		"x/ok.ts":     "import { A } from './broken';\nexport function f(a: A) { a.m(); this.z(); }\n",
		"x/odd.js":    "const { a: { b } } = require('./ok');\nb.c.d();\n(function () { this.x(); })();\n",
	}
	r := newTestRepo(t, files)
	r.index()
	// Sources now differ from the indexed sizes: receiver typing must fall
	// back gracefully.
	r.files["x/ok.ts"] = "// changed\n"
	r.resolveAll(Options{})
	if len(r.edges()) == 0 {
		t.Fatal("no edges")
	}
}

// TestTSBarrelValueImports resolves calls on names imported through barrel
// files (export * from / export { X } from, chained) and through namespace
// imports of a barrel at T1, via the framework's ReExporter hook.
func TestTSBarrelValueImports(t *testing.T) {
	r := fullBuild(t, map[string]string{
		"src/lib/math.ts":  "export function add(a: number, b: number): number { return a + b; }\nexport class Calc { static twice(x: number): number { return x * 2; } }\n",
		"src/lib/index.ts": "export * from './math';\n",
		"src/index.ts":     "export { add, Calc } from './lib';\n",
		"src/app.ts": `import { add, Calc } from './index';
import * as lib from './lib';

export function run(): number {
  return add(1, 2) + Calc.twice(3) + lib.add(4, 5) + missing();
}
`,
	}, nil)
	checkTSCases(t, r, []tsCase{
		{"src/app.ts::run", "add", "src/lib/math.ts::add", 100},
		{"src/app.ts::run", "twice", "src/lib/math.ts::Calc.twice", 80}, // static access: receiver-typed
		{"src/app.ts::run", "missing", "", 0},
	})
	if _, ok := For(model.LangTypeScript).(ReExporter); !ok {
		t.Error("TS resolver does not implement ReExporter")
	}
}
