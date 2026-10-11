<?php

declare(strict_types=1);

namespace App\Model;

class Product implements Priceable
{
    public function __construct(
        protected string $sku,
        protected int $cents,
    ) {
    }

    public function price(): int
    {
        return $this->cents;
    }

    public function sku(): string
    {
        return $this->sku;
    }
}
