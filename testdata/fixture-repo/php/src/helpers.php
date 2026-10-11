<?php

declare(strict_types=1);

namespace App;

function format_money(int $cents): string
{
    return sprintf('%d.%02d', intdiv($cents, 100), $cents % 100);
}
