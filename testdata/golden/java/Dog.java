package com.example.zoo;

public class Dog extends Animal {
    public Dog(String name) {
        super(name);
    }

    @Override
    public String sound() {
        return "woof";
    }

    public String fetch(String item) {
        return describe() + " and fetches " + item;
    }
}
