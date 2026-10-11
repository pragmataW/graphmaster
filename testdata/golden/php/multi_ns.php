<?php

namespace Legacy {
    function helper($a)
    {
        return strlen($a);
    }

    class Widget
    {
        var $name;

        function render()
        {
            echo helper($this->name);
            echo \Other\render_widget($this);
        }
    }
}

namespace {
    function global_fn(): void
    {
        Legacy\helper(1);
        $w = new Legacy\Widget();
    }
}
