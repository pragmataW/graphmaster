# MCP tools

`graphmaster mcp` serves 13 tools over stdio. Agents start it themselves
(`graphmaster install` registers it as the MCP server `graphmaster`). Every
tool is also available from the shell as `graphmaster query <tool>`, which
prints exactly the same text; the samples below were produced that way on
`testdata/fixture-repo`.

## Common behaviour

- **Fresh answers.** Before each call the server runs the incremental dirty
  check and reindexes changed files, waiting at most `[timeouts] mcp`
  (default 20 s) for another writer. The first line is a banner:

  | Banner | Meaning |
  |---|---|
  | `graphmaster callers · fresh` | nothing changed since the last scan |
  | `graphmaster impact · fresh (+1 files, 6ms)` | 1 file was reindexed before answering |
  | `graphmaster find_symbol · ⚠ stale: 3 files pending` | another process holds the write lock; answered from the current graph |
  | `graphmaster search · ⚠ building 37% (partial graph)` | the first build is still running |

- **Token budget.** Every tool takes `max_tokens` (default 1500, maximum
  6000, estimated as bytes / 4). Cut lists end with
  `… +N more (narrow path_prefix / raise limit)`.
- **Symbol lines** read `<kind> <qname>  <path>:<start>-<end>  <signature>`.
  Qualified names drop the prefix the path already implies (`service.OrderService.PlaceOrder`
  rather than the full Go import path).
- **Symbol arguments** accept a qualified name or a dotted suffix of one
  (`OrderService.PlaceOrder`), `path:line` (`go/internal/service/order.go:24`),
  a bare name (`PlaceOrder`) or a 16-hex-digit `skey`. An ambiguous argument
  returns a numbered candidate list with skeys to pick from:

  ```text
  graphmaster inheritance · fresh
  ambiguous symbol "Animal": 2 candidates; pass a qname, path:line or skey:
   1. class com.example.zoo.Animal  java/src/main/java/com/example/zoo/Animal.java:6-23  skey 0acf462d7a2ff20c
   2. constructor com.example.zoo.Animal.Animal  java/src/main/java/com/example/zoo/Animal.java:9-11  skey 1dab4582b4086b84
  ```

- **Confidence.** Graph edges carry a confidence from 1 to 100 (see
  [languages.md](languages.md#how-resolution-works)). Edges below 80 are
  prefixed with `~`.
- **Multiple repositories.** The repository enclosing the agent's working
  directory answers by default. A tool given an absolute path inside another
  git repository answers from that repository (its index is opened and
  freshened on demand).
- **Errors** are returned as MCP tool errors with a short explanation, for
  example `not a git repository; run graphmaster index --force`. The server
  never crashes on a tool call.
- **Server instructions.** The MCP `initialize` response carries a short
  usage guide (which tool answers which question), so agents without an
  instruction file, such as Cursor, still learn how to use the tools.

On the command line, arguments become flags with dashes
(`min_confidence` → `--min-confidence`) and the main argument may be given
positionally: `graphmaster query callers PlaceOrder --depth 2`.

## find_symbol

Find definitions by name: exact matches first, then prefix and fuzzy
matches; exported before private.

| Argument | Default | |
|---|---|---|
| `query` | required | name, qualified name, prefix or substring |
| `kind` | | only this kind: `function`, `method`, `class`, `struct`, `interface`, ... |
| `lang` | | only this language: `go`, `python`, `typescript`, ... |
| `path_prefix` | | only files under this repo-relative prefix |
| `exact` | false | exact name matches only |
| `limit` | 20 | |

```text
$ graphmaster query find_symbol PlaceOrder
graphmaster find_symbol · fresh
method service.OrderService.PlaceOrder  go/internal/service/order.go:24-32  func (s *OrderService) PlaceOrder(id string, qty int) (string, error)
method shop.services.orders.OrderService.place_order  python/shop/services/orders.py:16-21  def place_order(self, sku: str, qty: int) -> int
test service.TestPlaceOrder  go/internal/service/order_test.go:9-21  func TestPlaceOrder(t *testing.T)
test tests.test_orders.test_place_order  python/tests/test_orders.py:14-16  def test_place_order()
test service.TestPlaceOrderOutOfStock  go/internal/service/order_test.go:23-28  func TestPlaceOrderOutOfStock(t *testing.T)
test tests.test_orders.test_place_order_out_of_stock  python/tests/test_orders.py:19-21  def test_place_order_out_of_stock()
```

```text
$ graphmaster query find_symbol area --lang cpp
graphmaster find_symbol · fresh
method geo::Rect::area  cpp/include/geo/shape.hpp:17-17  double area() const override
method geo::Rect::area  cpp/src/shape.cpp:13-15  double Rect::area() const
method geo::Shape::area  cpp/include/geo/shape.hpp:10-10  virtual double area() const = 0
method geo::Canvas::totalArea  cpp/include/geo/canvas.hpp:13-13  double totalArea() const
method geo::Canvas::totalArea  cpp/src/canvas.cpp:9-15  double Canvas::totalArea() const
test test_total_area  cpp/tests/canvas_test.cpp:6-10  void test_total_area()
```

## search

Full-text (camelCase and snake_case aware) and substring search over symbol
names and file paths.

| Argument | Default | |
|---|---|---|
| `query` | required | words or identifier fragments |
| `kinds` | | restrict to these definition kinds; include `file` for file names |
| `path_prefix` | | |
| `limit` | 30 | |

```text
$ graphmaster query search order
graphmaster search · fresh
symbols (9):
class shop.services.orders.OrderService  python/shop/services/orders.py:12-24  class OrderService
struct service.OrderService  go/internal/service/order.go:14-16  type OrderService struct
method service.OrderService.PlaceOrder  go/internal/service/order.go:24-32  func (s *OrderService) PlaceOrder(id string, qty int) (string, error)
method shop.services.orders.OrderService.place_order  python/shop/services/orders.py:16-21  def place_order(self, sku: str, qty: int) -> int
function service.NewOrderService  go/internal/service/order.go:19-21  func NewOrderService(s store.Store) *OrderService
test service.TestPlaceOrder  go/internal/service/order_test.go:9-21  func TestPlaceOrder(t *testing.T)
test tests.test_orders.test_place_order  python/tests/test_orders.py:14-16  def test_place_order()
test service.TestPlaceOrderOutOfStock  go/internal/service/order_test.go:23-28  func TestPlaceOrderOutOfStock(t *testing.T)
test tests.test_orders.test_place_order_out_of_stock  python/tests/test_orders.py:19-21  def test_place_order_out_of_stock()
files (4):
file python/tests/test_orders.py  python, 28 loc
file go/internal/service/order.go  go, 37 loc
file python/shop/services/orders.py  python, 24 loc
file go/internal/service/order_test.go  go, 28 loc
```

## repo_map

Directory / package tree with file counts, lines of code and the most
referenced exported symbols. The cheapest way to get oriented.

| Argument | Default | |
|---|---|---|
| `path_prefix` | repository root | directory to map |
| `depth` | 2 | directory levels |

```text
$ graphmaster query repo_map --depth 1
graphmaster repo_map · fresh
./  65 files, 1122 loc
  c/  6 files, 89 loc  · Vec2, clamp_int, log_msg
  cpp/  6 files, 121 loc  · add, area, totalArea
  csharp/  7 files, 94 loc  · Invoice, CardPayment, IPayment
  go/  7 files, 177 loc  · Item, Memory, Store
  java/  7 files, 125 loc  · Animal, Cat, Keeper
  js/  5 files, 79 loc  · formatTotal
  php/  7 files, 134 loc
  python/  7 files, 112 loc  · Item, Repository, OrderService
  rust/  7 files, 94 loc  · Shape, square_of, Circle
  ts/  6 files, 97 loc  · User, Role, Admin
```

## file_outline

Nested definitions of a file with line ranges and signatures. Use it before
reading a large file.

| Argument | Default | |
|---|---|---|
| `path` | required | repo-relative file path |
| `depth` | 2 | nesting levels |
| `include_private` | true | include non-exported definitions |

```text
$ graphmaster query file_outline go/internal/service/order.go
graphmaster file_outline · fresh
go/internal/service/order.go  go, 37 loc, 6 symbols
variable ErrOutOfStock  11-11  var ErrOutOfStock = errors.New("out of stock")
struct OrderService  14-16  type OrderService struct
  field store  15-15  store store.Store
  method PlaceOrder  24-32  func (s *OrderService) PlaceOrder(id string, qty int) (string, error)
function NewOrderService  19-21  func NewOrderService(s store.Store) *OrderService
function validate  35-37  func validate(item store.Item, qty int) bool
```

## read_symbol

Source of one definition with line numbers.

| Argument | Default | |
|---|---|---|
| `symbol` | required | |
| `context_lines` | 0 | extra lines before and after |
| `max_lines` | 200 | |

```text
$ graphmaster query read_symbol OrderService.PlaceOrder
graphmaster read_symbol · fresh
method service.OrderService.PlaceOrder  go/internal/service/order.go:24-32  func (s *OrderService) PlaceOrder(id string, qty int) (string, error)
24  func (s *OrderService) PlaceOrder(id string, qty int) (string, error) {
25  	item, ok := s.store.Get(id)
26  	if !ok || !validate(item, qty) {
27  		return "", ErrOutOfStock
28  	}
29  	item.Quantity -= qty
30  	s.store.Put(item)
31  	return util.FormatID(id) + " " + util.FormatMoney(item.Price*qty), nil
32  }
```

## callers and callees

Call trees. `callers` lists the definitions that call a symbol (or reference
it, for non-callables such as types and fields); `callees` lists what a
symbol calls, plus its unresolved library calls. `@N` is the line of the call
site.

Calls through an interface or base-class method count as virtual dispatch:
`callers` of a concrete method also lists the callers of the interface or
base methods it implements or overrides, marked `~via <method>`, and
`callees` lists below an interface or base method the methods it may
dispatch to (`~implemented by`, at most five named). For Go these links come
from the derived implicit `implements` edges (see
[languages.md](languages.md#per-language-rules)); for languages with
explicit inheritance, from same-named methods of resolved supertypes.
`impact` follows the same links and shows definitions reached only through
dispatch at confidence 70 (`~`).

| Argument | Default | |
|---|---|---|
| `symbol` | required | |
| `depth` | 1 | levels to follow, maximum 5 |
| `min_confidence` | 50 | 1–100 |
| `limit` | 50 | maximum symbols |

```text
$ graphmaster query callers PlaceOrder
graphmaster callers · fresh
method service.OrderService.PlaceOrder  go/internal/service/order.go:24-32  func (s *OrderService) PlaceOrder(id string, qty int) (string, error)
  function server.main  go/cmd/server/main.go:14-24  @19
  test service.TestPlaceOrder  go/internal/service/order_test.go:9-21  @14
  test service.TestPlaceOrderOutOfStock  go/internal/service/order_test.go:23-28  @25
```

```text
$ graphmaster query callers store.Memory.Get
graphmaster callers · fresh
method store.Memory.Get  go/internal/store/store.go:23-28  func (m *Memory) Get(id string) (Item, bool)
  method service.OrderService.PlaceOrder  go/internal/service/order.go:24-32  @25  ~via store.Store.Get
```

```text
$ graphmaster query callees PlaceOrder
graphmaster callees · fresh
method service.OrderService.PlaceOrder  go/internal/service/order.go:24-32  func (s *OrderService) PlaceOrder(id string, qty int) (string, error)
  function service.validate  go/internal/service/order.go:35-37  @26
  method store.Store.Get  go/internal/store/store.go:7-7  @25
    ~implemented by store.Memory.Get
  method store.Store.Put  go/internal/store/store.go:8-8  @30
    ~implemented by store.Memory.Put, store.Audited.Put
  function util.FormatID  go/internal/util/format.go:11-13  @31
  function util.FormatMoney  go/internal/util/format.go:16-18  @31
```

A low-confidence edge (a virtual call through a base class, resolved by name
only):

```text
$ graphmaster query callers geo::Shape::area --min-confidence 10
graphmaster callers · fresh
method geo::Shape::area  cpp/include/geo/shape.hpp:10-10  virtual double area() const = 0
  ~method geo::Canvas::totalArea  cpp/src/canvas.cpp:9-15  @12
```

## impact

What may break if a symbol, a file or the uncommitted changes are modified:
the reverse transitive closure over calls, references, inheritance and
tests, grouped by file, with `dN` = distance and the number of affected
tests. Exactly one of `symbol`, `path` and `changed` selects the start.

| Argument | Default | |
|---|---|---|
| `symbol` | | |
| `path` | | repo-relative file path |
| `changed` | false | start from files changed against `HEAD` / the git index |
| `depth` | 3 | maximum 6 |
| `min_confidence` | 30 | |

```text
$ graphmaster query impact FormatMoney
graphmaster impact · fresh
impact of function util.FormatMoney  go/internal/util/format.go:16-18
depth 3, min_confidence 30: 4 symbols in 3 files · affected tests: 2
go/internal/service/order.go
  method service.OrderService.PlaceOrder  24-32  d1
go/cmd/server/main.go
  function server.main  14-24  d2
go/internal/service/order_test.go
  test service.TestPlaceOrder  9-21  d2
  test service.TestPlaceOrderOutOfStock  23-28  d2
```

After editing `go/internal/util/format.go` (note the banner: the edit was
indexed before answering):

```text
$ graphmaster query impact --changed
graphmaster impact · fresh (+1 files, 6ms)
impact of 1 changed files: go/internal/util/format.go (modified)
depth 3, min_confidence 30: 4 symbols in 3 files · affected tests: 2
go/cmd/server/main.go
  function server.main  14-24  d1
go/internal/service/order.go
  method service.OrderService.PlaceOrder  24-32  d1
go/internal/service/order_test.go
  test service.TestPlaceOrder  9-21  d2
  test service.TestPlaceOrderOutOfStock  23-28  d2
```

## imports

Imports of a file or package (`direction=out`), or its importers
(`direction=in`).

| Argument | Default | |
|---|---|---|
| `path` | | repo-relative file path or package directory |
| `module` | | package / module name or import spec |
| `direction` | `out` | `out` or `in` |

```text
$ graphmaster query imports go/internal/service/order.go
graphmaster imports · fresh
imports of go/internal/service/order.go (3):
  4  errors  (external)
  6  example.com/fixture/internal/store  → go/internal/store/
  7  example.com/fixture/internal/util  → go/internal/util/
```

```text
$ graphmaster query imports go/internal/util --direction in
graphmaster imports · fresh
importers of example.com/fixture/internal/util (2):
  go/cmd/server/main.go:11  example.com/fixture/internal/util
  go/internal/service/order.go:7  example.com/fixture/internal/util
```

## inheritance

Type hierarchy around a type.

| Argument | Default | |
|---|---|---|
| `symbol` | required | a type |
| `direction` | `both` | `up` (supertypes), `down` (subtypes) or `both` |
| `depth` | 3 | |

```text
$ graphmaster query inheritance com.example.zoo.Animal
graphmaster inheritance · fresh
class com.example.zoo.Animal  java/src/main/java/com/example/zoo/Animal.java:6-23  public abstract class Animal implements Named
supertypes:
  implements interface com.example.zoo.Named  java/src/main/java/com/example/zoo/Named.java:4-6
subtypes:
  extended by class com.example.zoo.Cat  java/src/main/java/com/example/zoo/Cat.java:3-20
  extended by class com.example.zoo.Dog  java/src/main/java/com/example/zoo/Dog.java:3-16
```

## tests_for

Tests that reach a symbol within two hops, plus tests named after it
(`TestPlaceOrder`, `test_place_order`).

| Argument | Default | |
|---|---|---|
| `symbol` | required | |

```text
$ graphmaster query tests_for PlaceOrder
graphmaster tests_for · fresh
tests for method service.OrderService.PlaceOrder  go/internal/service/order.go:24-32
reaching it within 2 hops (2):
  test service.TestPlaceOrder  go/internal/service/order_test.go:9-21  d1
  test service.TestPlaceOrderOutOfStock  go/internal/service/order_test.go:23-28  d1
by name: none
```

## traverse

Breadth-first exploration around a symbol over chosen edge kinds, ranked by
confidence and specificity (edges to highly connected hubs rank lower, and
hubs are summarized instead of expanded).

| Argument | Default | |
|---|---|---|
| `start` | required | |
| `edge_kinds` | `calls, references, extends, implements, embeds, tests, declares` | |
| `direction` | `out` | `out`, `in` or `both` |
| `max_depth` | 4 | maximum 8 |
| `min_confidence` | 50 | |

```text
$ graphmaster query traverse PlaceOrder --max-depth 2
graphmaster traverse · fresh
method service.OrderService.PlaceOrder  go/internal/service/order.go:24-32  func (s *OrderService) PlaceOrder(id string, qty int) (string, error)
out via calls,references,extends,implements,embeds,tests,declares, depth ≤2, min_confidence 50: 11 symbols
  →calls function util.FormatID  go/internal/util/format.go:11-13
  →calls function util.FormatMoney  go/internal/util/format.go:16-18
  →references struct service.OrderService  go/internal/service/order.go:14-16
  →calls method store.Store.Get  go/internal/store/store.go:7-7
    →references struct store.Item  go/internal/store/item.go:4-8
  →calls method store.Store.Put  go/internal/store/store.go:8-8
  →references field service.OrderService.store  go/internal/service/order.go:15-15
    →references interface store.Store  go/internal/store/store.go:6-9
  →references field store.Item.Price  go/internal/store/item.go:6-6
  →references field store.Item.Quantity  go/internal/store/item.go:7-7
  →calls function service.validate  go/internal/service/order.go:35-37
```

## status

Index state, file statuses, languages, database size, last scan and steering
mode. Call it when results look stale or empty.

```text
$ graphmaster query status
graphmaster status · fresh
repository: /home/me/src/fixture-repo
state: ready
mode: balanced
last scan: 2026-10-12T00:58:57+03:00 (5s ago)
files: 65 (ok 64, generated 1)
languages: python 7 rust 7 java 7 csharp 7 php 7 go 6 c 6 cpp 6 typescript 5 javascript 5 tsx 1
symbols: 236, edges: 604 (148 unresolved), pending names: 0
db size: 384.0 KiB (cache 44.0 KiB)
```

`unresolved` edges are references to code outside the repository (standard
library, dependencies) or names too ambiguous to guess. `graphmaster status`
prints the same report without freshening first.
