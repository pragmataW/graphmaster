package com.example.zoo;

import static org.junit.jupiter.api.Assertions.assertEquals;

import org.junit.jupiter.api.Test;

class DogTest {
    @Test
    void describeIncludesSound() {
        Dog dog = new Dog("rex");
        assertEquals("Rex says woof", dog.describe());
    }

    @Test
    void fetchMentionsItem() {
        assertEquals("Rex says woof and fetches ball", new Dog("rex").fetch("ball"));
    }
}
