package com.example.zoo.keeper;

import com.example.zoo.Animal;
import com.example.zoo.Cat;
import com.example.zoo.Dog;
import java.util.ArrayList;
import java.util.List;

public class Keeper {
    private final List<Animal> animals = new ArrayList<>();

    public void admit(Animal animal) {
        animals.add(animal);
    }

    public List<String> rollCall() {
        List<String> out = new ArrayList<>();
        for (Animal a : animals) {
            out.add(a.describe());
        }
        return out;
    }

    public static void main(String[] args) {
        Keeper keeper = new Keeper();
        keeper.admit(new Dog("rex"));
        keeper.admit(new Cat("tom", 9));
        keeper.rollCall().forEach(System.out::println);
    }
}
