<?php

namespace Tests\Shop;

use App\Shop\Cart;
use PHPUnit\Framework\Attributes\Test;
use PHPUnit\Framework\TestCase;

final class CartTest extends TestCase
{
    private Cart $cart;

    protected function setUp(): void
    {
        $this->cart = new Cart();
    }

    public function testTotalIsZero(): void
    {
        $this->assertSame(0, $this->cart->total());
    }

    #[Test]
    public function addsProducts(): void
    {
        $this->cart->add(make_product());
        self::assertCount(1, $this->cart);
    }

    #[\PHPUnit\Framework\Attributes\Test]
    public function it_formats(): void
    {
    }

    /** @test */
    public function annotatedWithDocblock(): void
    {
    }

    public function test_formats_money(): void
    {
    }

    private function helper(): void
    {
    }
}

function make_product(): \App\Model\Product
{
    return new \App\Model\Product('a', 1);
}
