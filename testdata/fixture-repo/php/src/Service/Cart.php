<?php

declare(strict_types=1);

namespace App\Service;

use App\Model\Priceable;
use App\Model\Product;

use function App\format_money;

class Cart
{
    /** @var Priceable[] */
    private array $items = [];

    public function add(Product $product): void
    {
        $this->items[] = $product;
    }

    public function total(): int
    {
        $sum = 0;
        foreach ($this->items as $item) {
            $sum += $item->price();
        }
        return $this->applyCoupon($sum);
    }

    public function summary(): string
    {
        return format_money($this->total());
    }

    private function applyCoupon(int $sum): int
    {
        return $sum > 10000 ? $sum - 500 : $sum;
    }
}
