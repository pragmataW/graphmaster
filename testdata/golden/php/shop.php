<?php

declare(strict_types=1);

namespace App\Shop;

use App\Model\Product;
use App\Model\{Priceable, Discount as Promo};
use function App\Util\format_money;
use function App\Util\{slug, trim_all};
use const App\Config\CURRENCY;
use Psr\Log\LoggerInterface as Logger;

const TAX_RATE = 0.2;

interface HasTotal extends \Countable, Priceable
{
    public function total(): int;
}

trait Loggable
{
    protected ?Logger $logger = null;

    public function log(string $msg): void
    {
        $this->logger?->info($msg);
    }
}

enum Status: string implements HasLabel
{
    case Open = 'open';
    case Closed = 'closed';

    public function label(): string
    {
        return ucfirst($this->value);
    }
}

abstract class Base
{
    public const LIMIT = 10;

    abstract protected function items(): array;
}

final class Cart extends Base implements HasTotal, \JsonSerializable
{
    use Loggable, \App\Traits\Cacheable;

    private array $lines = [];
    public static int $count = 0, $made = 0;

    public function __construct(private Product $first, protected readonly Promo $promo)
    {
        parent::__construct();
        self::$count++;
    }

    public function add(Product $product, ?Promo $promo = null): static
    {
        $this->lines[] = $product;
        $product->rename(slug($product->name));
        $promo?->apply($product);
        static::bump();
        return $this;
    }

    public function total(): int
    {
        $sum = 0;
        $calc = new Calculator(TAX_RATE);
        foreach ($this->lines as $line) {
            $sum += $line->price();
        }
        $sum = $calc->apply($sum) + Promo::flat() + \App\Model\Discount::LIMIT;
        $this->log(format_money($sum, CURRENCY));
        return \App\Util\round_cents($sum) + Util\cents($sum) + count($this->lines);
    }

    public function jsonSerialize(): mixed
    {
        return ['total' => $this->total(), 'first' => $this->first->sku()];
    }

    protected function items(): array
    {
        return $this->lines;
    }

    private static function bump(): void
    {
        $fn = function (int $x) use (&$fn) {
            return $x + 1;
        };
        $arrow = fn($y) => new Product('x', $y);
    }
}

function make_cart(Product $first): Cart
{
    return new Cart($first, new Promo());
}
