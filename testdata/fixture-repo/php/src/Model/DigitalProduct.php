<?php

declare(strict_types=1);

namespace App\Model;

final class DigitalProduct extends Product
{
    public function price(): int
    {
        return (int) round(parent::price() * 0.8);
    }
}
