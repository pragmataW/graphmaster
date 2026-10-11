# Expected graph for fixture-repo

This file lists the key symbols and edges graphmaster must extract from this
fixture once it has been materialized by `internal/testutil.Materialize`. It
is the human-readable source for `expected_edges.txt` (resolver tests).

## Notation

Edge lines inside the `edges` blocks have the form

```
[prefix]<kind> <src> -> <dst> [conf>=N]
```

- `<src>` / `<dst>` are `path::Symbol`. `Symbol` is the language-neutral
  container chain joined with `.` (e.g. `OrderService.PlaceOrder`,
  `geo.Rect.area`); each language maps it onto its own qname format.
  `external:<name>` marks a target outside the indexed code (stdlib, vendor,
  third-party), which must stay unresolved (`dst = NULL`).
- `conf>=N` is the minimum acceptable confidence (100 = T1 import-qualified,
  90 = T2 lexical, 80 = T3 receiver-typed, 50 = T4 unique global).
- Prefix `?` = desirable but optional (dynamic dispatch, inference beyond v1).
  Prefix `!` = this edge must NOT exist.
- Kinds: `calls`, `extends`, `implements`, `embeds`, `imports`, `includes`,
  `declares`, `tests`.

## File statuses

| Path | Tracked | Expected |
|---|---|---|
| `go/internal/gen/status_string.go` | yes | `generated` (no nodes) |
| `js/public/widgets.min.js` | yes | excluded by the default `*.min.*` config exclude (no files row); its content alone classifies as `minified` |
| `python/shop/catalog_big.py` | yes (2nd commit, created by Materialize) | `too_big` (> 1 MiB, no nodes) |
| `go/vendor/**`, `php/vendor/**` | yes (2nd commit, created by Materialize) | excluded by default config; no nodes |
| `go/internal/util/slug.go` | untracked | `ok`, indexed |
| `python/shop/experimental.py` | untracked | `ok`, indexed |
| `notes/scratch.md`, `logs/important.log`, `js/keep.tmp` | untracked | not code |
| `debug.log`, `.env`, `build/output.js`, `ts/dist/index.js`, `js/cache.tmp`, `python/shop/__pycache__/*.pyc` | ignored | never indexed |
| everything else under a language dir | yes | `ok` |

Ignore rules exercised: root `.gitignore` (directory patterns, globs,
`!important.log` negation) and nested `js/.gitignore` (`*.tmp`,
`!keep.tmp`).

---

## Go (`go/`, module `example.com/fixture`)

Symbols:

- `go/internal/store/item.go`: struct `Item`, method `Item.Total`
- `go/internal/store/store.go`: interface `Store` (`Get`, `Put`), struct
  `Memory`, func `NewMemory`, methods `Memory.Get`, `Memory.Put`, struct
  `Audited` (embeds `*Memory`), func `NewAudited`, method `Audited.Put`
- `go/internal/util/format.go`: `FormatID`, `FormatMoney`, `Mirror`
- `go/internal/util/slug.go` (untracked): `Slug`
- `go/internal/service/order.go`: var `ErrOutOfStock`, struct `OrderService`,
  `NewOrderService`, method `OrderService.PlaceOrder`, unexported `validate`
- `go/internal/service/order_test.go`: tests `TestPlaceOrder`,
  `TestPlaceOrderOutOfStock`
- `go/cmd/server/main.go`: `main`

```edges
imports go/cmd/server/main.go -> go/internal/store
imports go/cmd/server/main.go -> go/internal/service
imports go/cmd/server/main.go -> go/internal/util
imports go/internal/service/order.go -> go/internal/store
imports go/internal/service/order.go -> go/internal/util
calls go/cmd/server/main.go::main -> go/internal/store/store.go::NewAudited conf>=100
calls go/cmd/server/main.go::main -> go/internal/service/order.go::NewOrderService conf>=100
calls go/cmd/server/main.go::main -> go/internal/util/format.go::Mirror conf>=100
calls go/cmd/server/main.go::main -> go/internal/service/order.go::OrderService.PlaceOrder conf>=50
calls go/internal/service/order.go::OrderService.PlaceOrder -> go/internal/service/order.go::validate conf>=90
calls go/internal/service/order.go::OrderService.PlaceOrder -> go/internal/util/format.go::FormatID conf>=100
calls go/internal/service/order.go::OrderService.PlaceOrder -> go/internal/util/format.go::FormatMoney conf>=100
?calls go/internal/service/order.go::OrderService.PlaceOrder -> go/internal/store/store.go::Store.Get conf>=80
calls go/internal/store/store.go::NewAudited -> go/internal/store/store.go::NewMemory conf>=90
calls go/internal/store/store.go::Audited.Put -> go/internal/store/store.go::Memory.Put conf>=80
calls go/internal/util/slug.go::Slug -> go/internal/util/format.go::FormatID conf>=90
calls go/internal/service/order_test.go::TestPlaceOrder -> go/internal/service/order.go::NewOrderService conf>=90
calls go/internal/service/order_test.go::TestPlaceOrder -> go/internal/store/store.go::NewMemory conf>=100
calls go/internal/service/order_test.go::TestPlaceOrder -> go/internal/service/order.go::OrderService.PlaceOrder conf>=50
calls go/internal/util/format.go::Mirror -> external:strutil.Reverse
embeds go/internal/store/store.go::Audited -> go/internal/store/store.go::Memory
?implements go/internal/store/store.go::Memory -> go/internal/store/store.go::Store
?tests go/internal/service/order_test.go::TestPlaceOrder -> go/internal/service/order.go::OrderService.PlaceOrder
!calls go/cmd/server/main.go::main -> go/internal/gen/status_string.go::Status.String
!calls go/internal/util/format.go::Mirror -> go/vendor/github.com/acme/strutil/strutil.go::Reverse
```

## Python (`python/`, package `shop`)

Symbols:

- `python/shop/models.py`: class `Item` (`total`, `describe`), class
  `PerishableItem(Item)` (`total`)
- `python/shop/repository.py`: class `Repository` (`__init__`, `add`, `get`)
- `python/shop/services/pricing.py`: `apply_discount`, `round_cents`,
  constant `DISCOUNT_THRESHOLD`
- `python/shop/services/orders.py`: class `OutOfStock(Exception)`, class
  `OrderService` (`__init__`, `place_order`, `_can_fulfil`)
- `python/shop/experimental.py` (untracked): `flash_sale`
- `python/tests/test_orders.py`: `make_service`, tests `test_place_order`,
  `test_place_order_out_of_stock`, class `TestPricing` with
  `test_discount_applies`

```edges
imports python/shop/services/orders.py -> python/shop/models.py
imports python/shop/services/orders.py -> python/shop/repository.py
imports python/shop/services/orders.py -> python/shop/services/pricing.py
imports python/shop/__init__.py -> python/shop/models.py
imports python/tests/test_orders.py -> python/shop/services/orders.py
extends python/shop/models.py::PerishableItem -> python/shop/models.py::Item
extends python/shop/services/orders.py::OutOfStock -> external:Exception
calls python/shop/services/orders.py::OrderService.place_order -> python/shop/services/orders.py::OrderService._can_fulfil conf>=80
calls python/shop/services/orders.py::OrderService.place_order -> python/shop/services/pricing.py::apply_discount conf>=100
?calls python/shop/services/orders.py::OrderService.place_order -> python/shop/repository.py::Repository.get conf>=80
calls python/shop/services/pricing.py::apply_discount -> python/shop/services/pricing.py::round_cents conf>=90
calls python/shop/experimental.py::flash_sale -> python/shop/services/pricing.py::apply_discount conf>=100
calls python/tests/test_orders.py::test_place_order -> python/tests/test_orders.py::make_service conf>=90
calls python/tests/test_orders.py::TestPricing.test_discount_applies -> python/shop/services/pricing.py::apply_discount conf>=100
?calls python/shop/models.py::PerishableItem.total -> python/shop/models.py::Item.total conf>=80
?tests python/tests/test_orders.py::test_place_order -> python/shop/services/orders.py::OrderService.place_order
```

## TypeScript / TSX (`ts/`, alias `@app/*` -> `src/*`)

Symbols:

- `ts/src/models/user.ts`: interface `Entity`, type `Role`, class `User`
  (`constructor`, `role`), class `Admin` (`role`, `canDelete`)
- `ts/src/utils/format.ts`: function `formatName`, arrow const `capitalize`
- `ts/src/services/userService.ts`: class `UserService` (`add`, `find`,
  `greet`, `promote`)
- `ts/src/index.ts`: barrel re-exports
- `ts/src/components/UserCard.tsx`: components `UserCard`, `Badge`
- `ts/test/userService.test.ts`: `describe` / `it` test callbacks

```edges
imports ts/src/services/userService.ts -> ts/src/models/user.ts
imports ts/src/services/userService.ts -> ts/src/utils/format.ts
imports ts/src/components/UserCard.tsx -> ts/src/services/userService.ts
imports ts/src/index.ts -> ts/src/models/user.ts
imports ts/src/index.ts -> ts/src/services/userService.ts
imports ts/test/userService.test.ts -> ts/src/services/userService.ts
implements ts/src/models/user.ts::User -> ts/src/models/user.ts::Entity
extends ts/src/models/user.ts::Admin -> ts/src/models/user.ts::User
calls ts/src/services/userService.ts::UserService.greet -> ts/src/utils/format.ts::formatName conf>=100
calls ts/src/services/userService.ts::UserService.greet -> ts/src/services/userService.ts::UserService.find conf>=80
calls ts/src/services/userService.ts::UserService.promote -> ts/src/services/userService.ts::UserService.find conf>=80
calls ts/src/utils/format.ts::formatName -> ts/src/utils/format.ts::capitalize conf>=90
calls ts/src/models/user.ts::Admin.canDelete -> ts/src/models/user.ts::User.role conf>=80
calls ts/src/components/UserCard.tsx::UserCard -> ts/src/services/userService.ts::UserService.greet conf>=50
?calls ts/src/components/UserCard.tsx::UserCard -> ts/src/components/UserCard.tsx::Badge
?calls ts/test/userService.test.ts -> ts/src/services/userService.ts::UserService.greet conf>=50
```

## JavaScript (`js/`, CommonJS + ESM)

Symbols:

- `js/lib/math.js`: `add`, `multiply`
- `js/lib/calc.js`: class `Calculator` (`constructor`, `sum`, `product`,
  `record`), class `ScientificCalculator` (`square`)
- `js/src/format.mjs`: `formatTotal`, `pad`
- `js/src/main.mjs`: `run`
- `js/test/calc.test.js`: `test(...)` callbacks
- `js/public/widgets.min.js`: minified, no symbols

```edges
imports js/lib/calc.js -> js/lib/math.js
imports js/src/main.mjs -> js/src/format.mjs
imports js/test/calc.test.js -> js/lib/calc.js
?imports js/src/main.mjs -> js/lib/calc.js
extends js/lib/calc.js::ScientificCalculator -> js/lib/calc.js::Calculator
calls js/lib/math.js::multiply -> js/lib/math.js::add conf>=90
calls js/lib/calc.js::Calculator.sum -> js/lib/math.js::add conf>=100
calls js/lib/calc.js::Calculator.sum -> js/lib/calc.js::Calculator.record conf>=80
calls js/lib/calc.js::Calculator.product -> js/lib/math.js::multiply conf>=100
calls js/lib/calc.js::ScientificCalculator.square -> js/lib/calc.js::Calculator.product conf>=80
calls js/src/format.mjs::formatTotal -> js/src/format.mjs::pad conf>=90
calls js/src/main.mjs::run -> js/src/format.mjs::formatTotal conf>=100
?calls js/src/main.mjs::run -> js/lib/calc.js::ScientificCalculator.square conf>=80
?calls js/src/main.mjs::run -> js/lib/calc.js::Calculator.sum conf>=80
```

## Rust (`rust/`, crate `fixture_shapes`)

Symbols:

- `rust/src/shapes/mod.rs`: trait `Shape` (`area`, default `name`)
- `rust/src/shapes/circle.rs`: struct `Circle`, `Circle.new`, impl `Shape for
  Circle` (`area`, `name`)
- `rust/src/shapes/square.rs`: struct `Square`, `Square.new`, impl `Shape for
  Square` (`area`)
- `rust/src/geometry.rs`: `square_of`, `total_area`, test
  `tests.square_of_negative`
- `rust/src/main.rs`: `main`
- `rust/tests/area_test.rs`: test `squares_add_up`

```edges
implements rust/src/shapes/circle.rs::Circle -> rust/src/shapes/mod.rs::Shape
implements rust/src/shapes/square.rs::Square -> rust/src/shapes/mod.rs::Shape
calls rust/src/shapes/circle.rs::Circle.area -> rust/src/geometry.rs::square_of conf>=100
calls rust/src/shapes/square.rs::Square.area -> rust/src/geometry.rs::square_of conf>=100
calls rust/src/main.rs::main -> rust/src/geometry.rs::total_area conf>=100
calls rust/src/main.rs::main -> rust/src/shapes/circle.rs::Circle.new conf>=100
calls rust/src/main.rs::main -> rust/src/shapes/square.rs::Square.new conf>=100
calls rust/src/geometry.rs::tests.square_of_negative -> rust/src/geometry.rs::square_of conf>=90
calls rust/tests/area_test.rs::squares_add_up -> rust/src/geometry.rs::total_area conf>=100
calls rust/tests/area_test.rs::squares_add_up -> rust/src/shapes/square.rs::Square.new conf>=100
?calls rust/src/geometry.rs::total_area -> rust/src/shapes/mod.rs::Shape.area conf>=80
```

## Java (`java/`, package `com.example.zoo`)

Symbols:

- `Named` (interface: `name`), `Animal` (abstract: constructor, `name`,
  `sound`, `describe`), `Dog` (constructor, `sound`, `fetch`), `Cat`
  (constructor, `sound`, `compareTo`)
- `util/Strings.capitalize`, `keeper/Keeper` (`admit`, `rollCall`, `main`)
- test class `DogTest` (`describeIncludesSound`, `fetchMentionsItem`)

```edges
implements java/src/main/java/com/example/zoo/Animal.java::Animal -> java/src/main/java/com/example/zoo/Named.java::Named
extends java/src/main/java/com/example/zoo/Dog.java::Dog -> java/src/main/java/com/example/zoo/Animal.java::Animal
extends java/src/main/java/com/example/zoo/Cat.java::Cat -> java/src/main/java/com/example/zoo/Animal.java::Animal
implements java/src/main/java/com/example/zoo/Cat.java::Cat -> external:Comparable
imports java/src/main/java/com/example/zoo/keeper/Keeper.java -> java/src/main/java/com/example/zoo/Animal.java
calls java/src/main/java/com/example/zoo/Animal.java::Animal.name -> java/src/main/java/com/example/zoo/util/Strings.java::Strings.capitalize conf>=100
calls java/src/main/java/com/example/zoo/Animal.java::Animal.describe -> java/src/main/java/com/example/zoo/Animal.java::Animal.name conf>=80
calls java/src/main/java/com/example/zoo/Animal.java::Animal.describe -> java/src/main/java/com/example/zoo/Animal.java::Animal.sound conf>=80
calls java/src/main/java/com/example/zoo/Dog.java::Dog.fetch -> java/src/main/java/com/example/zoo/Animal.java::Animal.describe conf>=80
calls java/src/main/java/com/example/zoo/keeper/Keeper.java::Keeper.rollCall -> java/src/main/java/com/example/zoo/Animal.java::Animal.describe conf>=80
calls java/src/main/java/com/example/zoo/keeper/Keeper.java::Keeper.main -> java/src/main/java/com/example/zoo/keeper/Keeper.java::Keeper.admit conf>=80
calls java/src/main/java/com/example/zoo/keeper/Keeper.java::Keeper.main -> java/src/main/java/com/example/zoo/keeper/Keeper.java::Keeper.rollCall conf>=80
calls java/src/test/java/com/example/zoo/DogTest.java::DogTest.describeIncludesSound -> java/src/main/java/com/example/zoo/Animal.java::Animal.describe conf>=80
calls java/src/test/java/com/example/zoo/DogTest.java::DogTest.fetchMentionsItem -> java/src/main/java/com/example/zoo/Dog.java::Dog.fetch conf>=50
?calls java/src/main/java/com/example/zoo/keeper/Keeper.java::Keeper.main -> java/src/main/java/com/example/zoo/Dog.java::Dog.Dog
```

## C (`c/`)

Symbols:

- `c/include/vec.h`: typedef `Vec2`, macro `VEC2_ZERO`, declarations
  `vec_add`, `vec_scale`, `vec_len`
- `c/src/vec.c`: static `sq`, `vec_add`, `vec_scale`, `vec_len`
- `c/src/util.h` / `c/src/util.c`: `log_msg`, `clamp_int`
- `c/src/main.c`: `main`
- `c/tests/test_vec.c`: tests `test_vec_add`, `test_vec_len`, `main`

```edges
includes c/src/vec.c -> c/include/vec.h
includes c/src/main.c -> c/include/vec.h
includes c/src/main.c -> c/src/util.h
includes c/src/util.c -> c/src/util.h
includes c/tests/test_vec.c -> c/include/vec.h
declares c/include/vec.h::vec_add -> c/src/vec.c::vec_add
declares c/include/vec.h::vec_len -> c/src/vec.c::vec_len
declares c/src/util.h::log_msg -> c/src/util.c::log_msg
calls c/src/vec.c::vec_len -> c/src/vec.c::sq conf>=90
calls c/src/main.c::main -> c/src/vec.c::vec_add conf>=50
calls c/src/main.c::main -> c/src/vec.c::vec_scale conf>=50
calls c/src/main.c::main -> c/src/vec.c::vec_len conf>=50
calls c/src/main.c::main -> c/src/util.c::log_msg conf>=50
calls c/src/main.c::main -> c/src/util.c::clamp_int conf>=50
calls c/tests/test_vec.c::test_vec_add -> c/src/vec.c::vec_add conf>=50
calls c/tests/test_vec.c::main -> c/tests/test_vec.c::test_vec_add conf>=90
calls c/src/vec.c::vec_len -> external:sqrt
!calls c/src/main.c::main -> c/tests/test_vec.c::test_vec_add
```

## C++ (`cpp/`, namespace `geo`)

Symbols (qnames use `::` in C++; written with `.` here):

- `cpp/include/geo/shape.hpp`: namespace `geo`; classes `geo.Shape`
  (`~Shape`, `area`, `label`), `geo.Rect` (`Rect`, `area`), `geo.Square`
  (`Square`, `label`); `geo.util.clamp` declaration
- `cpp/src/shape.cpp`: out-of-line definitions `geo.Shape.~Shape`,
  `geo.Shape.label`, `geo.Rect.Rect`, `geo.Rect.area`, `geo.Square.Square`,
  `geo.Square.label`, `geo.util.clamp`
- `cpp/include/geo/canvas.hpp` / `cpp/src/canvas.cpp`: class `geo.Canvas`
  (`add`, `totalArea`, inline `size`); `canvas.cpp` defines methods under
  `using namespace geo`
- `cpp/src/main.cpp`: `main`; `cpp/tests/canvas_test.cpp`: `test_total_area`

```edges
extends cpp/include/geo/shape.hpp::geo.Rect -> cpp/include/geo/shape.hpp::geo.Shape
extends cpp/include/geo/shape.hpp::geo.Square -> cpp/include/geo/shape.hpp::geo.Rect
includes cpp/src/shape.cpp -> cpp/include/geo/shape.hpp
includes cpp/include/geo/canvas.hpp -> cpp/include/geo/shape.hpp
includes cpp/src/canvas.cpp -> cpp/include/geo/canvas.hpp
includes cpp/src/main.cpp -> cpp/include/geo/canvas.hpp
calls cpp/src/shape.cpp::geo.Rect.area -> cpp/src/shape.cpp::geo.util.clamp conf>=90
calls cpp/src/main.cpp::main -> cpp/src/canvas.cpp::geo.Canvas.add conf>=80
calls cpp/src/main.cpp::main -> cpp/src/canvas.cpp::geo.Canvas.totalArea conf>=80
calls cpp/src/main.cpp::main -> cpp/include/geo/canvas.hpp::geo.Canvas.size conf>=80
calls cpp/tests/canvas_test.cpp::test_total_area -> cpp/src/canvas.cpp::geo.Canvas.totalArea conf>=80
?calls cpp/src/canvas.cpp::geo.Canvas.totalArea -> cpp/include/geo/shape.hpp::geo.Shape.area conf>=80
?declares cpp/include/geo/shape.hpp::geo.Rect.area -> cpp/src/shape.cpp::geo.Rect.area
```

## C# (`csharp/`, namespace `Fixture`)

Symbols:

- `Fixture.Billing.IPayment` (`Charge`), `PaymentBase` (property `Fee`,
  `WithFee`), `CardPayment` (`Charge`)
- partial class `Fixture.Billing.Invoice` split across `Invoice.cs`
  (`_lines`, `AddLine`, `Total`) and `Invoice.Tax.cs` (`TaxRate`, `ApplyTax`)
- `Fixture.App.Program.Main`
- tests `Fixture.Tests.InvoiceTests` (`TotalIncludesTax`, `CardPaymentAddsFee`)

```edges
extends csharp/src/Billing/CardPayment.cs::Fixture.Billing.CardPayment -> csharp/src/Billing/PaymentBase.cs::Fixture.Billing.PaymentBase
implements csharp/src/Billing/CardPayment.cs::Fixture.Billing.CardPayment -> csharp/src/Billing/IPayment.cs::Fixture.Billing.IPayment
calls csharp/src/Billing/Invoice.cs::Fixture.Billing.Invoice.Total -> csharp/src/Billing/Invoice.Tax.cs::Fixture.Billing.Invoice.ApplyTax conf>=80
calls csharp/src/Billing/CardPayment.cs::Fixture.Billing.CardPayment.Charge -> csharp/src/Billing/PaymentBase.cs::Fixture.Billing.PaymentBase.WithFee conf>=80
calls csharp/src/App/Program.cs::Fixture.App.Program.Main -> csharp/src/Billing/Invoice.cs::Fixture.Billing.Invoice.AddLine conf>=80
calls csharp/src/App/Program.cs::Fixture.App.Program.Main -> csharp/src/Billing/Invoice.cs::Fixture.Billing.Invoice.Total conf>=80
calls csharp/src/App/Program.cs::Fixture.App.Program.Main -> csharp/src/Billing/IPayment.cs::Fixture.Billing.IPayment.Charge conf>=80
calls csharp/tests/InvoiceTests.cs::Fixture.Tests.InvoiceTests.TotalIncludesTax -> csharp/src/Billing/Invoice.cs::Fixture.Billing.Invoice.Total conf>=80
calls csharp/tests/InvoiceTests.cs::Fixture.Tests.InvoiceTests.CardPaymentAddsFee -> csharp/src/Billing/CardPayment.cs::Fixture.Billing.CardPayment.Charge conf>=80
```

## PHP (`php/`, PSR-4 `App\` -> `src/`)

Symbols:

- `App\Model\Priceable` (`price`), `App\Model\Product` (`__construct`,
  `price`, `sku`), `App\Model\DigitalProduct` (`price`)
- function `App\format_money` in `src/helpers.php`
- `App\Service\Cart` (`add`, `total`, `summary`, private `applyCoupon`)
- `bootstrap.php` (top-level script, `require_once`)
- test `Tests\CartTest` (`testTotalSumsPrices`, `testSummaryFormatsMoney`)

```edges
implements php/src/Model/Product.php::App.Model.Product -> php/src/Model/Priceable.php::App.Model.Priceable
extends php/src/Model/DigitalProduct.php::App.Model.DigitalProduct -> php/src/Model/Product.php::App.Model.Product
extends php/tests/CartTest.php::Tests.CartTest -> external:PHPUnit.Framework.TestCase
imports php/src/Service/Cart.php -> php/src/Model/Product.php
includes php/bootstrap.php -> php/src/helpers.php
calls php/src/Model/DigitalProduct.php::App.Model.DigitalProduct.price -> php/src/Model/Product.php::App.Model.Product.price conf>=80
calls php/src/Service/Cart.php::App.Service.Cart.total -> php/src/Service/Cart.php::App.Service.Cart.applyCoupon conf>=80
calls php/src/Service/Cart.php::App.Service.Cart.summary -> php/src/Service/Cart.php::App.Service.Cart.total conf>=80
calls php/src/Service/Cart.php::App.Service.Cart.summary -> php/src/helpers.php::App.format_money conf>=100
calls php/tests/CartTest.php::Tests.CartTest.testTotalSumsPrices -> php/src/Service/Cart.php::App.Service.Cart.total conf>=80
calls php/tests/CartTest.php::Tests.CartTest.testSummaryFormatsMoney -> php/src/Service/Cart.php::App.Service.Cart.summary conf>=80
?calls php/src/Service/Cart.php::App.Service.Cart.total -> php/src/Model/Priceable.php::App.Model.Priceable.price conf>=50
!includes php/bootstrap.php -> php/vendor/autoload.php
```
