<?php

declare(strict_types=1);

require_once __DIR__ . '/vendor/autoload.php';
require_once 'src/helpers.php';

$cart = new App\Service\Cart();
$cart->add(new App\Model\DigitalProduct('ebook-1', 1500));
echo $cart->summary(), PHP_EOL;
