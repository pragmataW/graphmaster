<html>
<body>
<?php
require_once __DIR__ . '/vendor/autoload.php';
require 'lib/helpers.php';
include_once dirname(__FILE__) . "/config.php";
include $dynamic . '.php';
require_once(__DIR__ . '/boot.inc');

function render_page($title)
{
    echo htmlspecialchars($title);
    $w = new Widget();
    $w->render();
    return Widget::create();
}
?>
</body>
</html>
