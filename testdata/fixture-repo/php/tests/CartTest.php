<?php

declare(strict_types=1);

namespace Tests;

use App\Model\Product;
use App\Service\Cart;
use PHPUnit\Framework\TestCase;

final class CartTest extends TestCase
{
    public function testTotalSumsPrices(): void
    {
        $cart = new Cart();
        $cart->add(new Product('a', 300));
        $cart->add(new Product('b', 200));
        $this->assertSame(500, $cart->total());
    }

    public function testSummaryFormatsMoney(): void
    {
        $cart = new Cart();
        $cart->add(new Product('a', 1234));
        $this->assertSame('12.34', $cart->summary());
    }
}
