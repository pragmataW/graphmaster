package com.example.zoo;

import com.example.zoo.util.Strings;

/** Base class for every animal in the zoo. */
public abstract class Animal implements Named {
    private final String name;

    protected Animal(String name) {
        this.name = name;
    }

    @Override
    public String name() {
        return Strings.capitalize(name);
    }

    public abstract String sound();

    public String describe() {
        return name() + " says " + sound();
    }
}
