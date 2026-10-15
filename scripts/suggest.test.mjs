import { suggest, baseDomain } from "../frontend/suggest.js";
import assert from "node:assert/strict";
assert.equal(baseDomain("a.b.example.com."), "example.com");
assert.equal(baseDomain("x.example.co.uk"), "example.co.uk");
assert.equal(baseDomain("example.com"), "example.com");
const f = (name, o = {}) => ({ name, duration_ns: 5e7, answers: ["1.1.1.1"], rcode: "NOERROR", ...o });
const fail = (n) => f(n, { error: "timeout", answers: [] });
let log = [fail("a.site.io"), fail("a.site.io."), fail("A.site.io"), f("ok.net"), f("nx.org", { rcode: "NXDOMAIN", answers: [] })];
let s = suggest(log, []);
assert.equal(s.length, 1); assert.equal(s[0].domain, "a.site.io"); assert.equal(s[0].count, 3); // exact queried name
assert.deepEqual(suggest(log, [{ domain: "site.io", enabled: true }]), []);          // parent rule covers it
assert.equal(suggest(log, [{ domain: "b.site.io", enabled: true }]).length, 1);      // a rule for a sibling/child does not
assert.equal(suggest(log, [{ domain: "x.a.site.io", enabled: true }]).length, 1);
assert.equal(suggest(log, [{ domain: "site.io", enabled: false }]).length, 1);       // disabled rule does not hide it
assert.deepEqual(suggest(log, [], new Set(["a.site.io"])), []);                         // ignored
assert.deepEqual(suggest([fail("a.x.io"), fail("a.x.io")], []), []);                  // below threshold
assert.deepEqual(suggest(Array(9).fill(f("n.org", { rcode: "NXDOMAIN", answers: [] })), []), []); // NXDOMAIN alone is normal
assert.deepEqual(suggest(Array(5).fill(fail("r.io")).map((e) => ({ ...e, rule: "r.io" })), []), []); // rule traffic ignored
const blk = Array(3).fill(f("g.com", { answers: ["0.0.0.0"] }));
assert.match(suggest(blk, [])[0].reason, /0\.0\.0\.0/);
assert.match(suggest(Array(3).fill(f("s.com", { duration_ns: 4e9 })), [])[0].reason, /медленно/);
assert.deepEqual(suggest(Array(5).fill(f("slow.nx", { rcode: "NXDOMAIN", answers: [], duration_ns: 4e9 })), []), []); // slow NXDOMAIN is not a failure
assert.deepEqual(suggest(null, null), []);
assert.deepEqual(suggest([null, {}], []), []);
console.log("suggest ok");
