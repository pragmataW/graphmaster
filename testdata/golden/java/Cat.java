package com.example.zoo;

public class Cat extends Animal implements Comparable<Cat> {
    private final int lives;

    public Cat(String name, int lives) {
        super(name);
        this.lives = lives;
    }

    @Override
    public String sound() {
        return "meow";
    }

    @Override
    public int compareTo(Cat other) {
        return Integer.compare(lives, other.lives);
    }
}
