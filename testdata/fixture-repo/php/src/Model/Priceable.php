<?php

declare(strict_types=1);

namespace App\Model;

interface Priceable
{
    public function price(): int;
}
