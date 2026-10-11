package com.example.features;

import static java.util.Objects.requireNonNull;
import static com.example.features.util.Checks.*;

import com.example.features.model.*;
import com.example.features.model.Shape.Kind;
import java.io.BufferedReader;
import java.util.List;
import java.util.function.Function;

/** Exercises every Java definition and reference shape. */
@SuppressWarnings("unchecked")
public class Features<T extends Comparable<T>> extends Base<T> implements Runnable, Function<String, T> {
    public static final int LIMIT = 10, MAX = 20;
    private final List<T> items;
    private Helper helper = new Helper();
    String[] names;

    public Features(List<T> items) {
        super(items.size());
        this.items = requireNonNull(items);
    }

    @Override
    public void run() {
        var copy = new Helper();
        copy.assist(LIMIT);
        helper.assist(MAX);
        this.helper.assist(1);
        super.reset();
        items.forEach(System.out::println);
        Runnable r = () -> helper.assist(2);
        Runnable anon = new Runnable() {
            @Override
            public void run() {
                assist(3);
            }
        };
        Object o = helper;
        if (o instanceof Helper h) {
            h.assist(4);
        }
        try (BufferedReader in = open()) {
            in.readLine();
        } catch (java.io.IOException e) {
            e.printStackTrace();
        }
        Shape.Kind k = Kind.ROUND;
        com.example.features.util.Checks.verify(k);
        Circle c = new Circle(1.0);
        c.area().toString();
    }

    @Override
    public T apply(String s) {
        return items.get(Integer.parseInt(s));
    }

    public static <R> R first(List<R> rs) {
        return rs.get(0);
    }

    private BufferedReader open() {
        return null;
    }

    void assist(int n) {
        checkPositive(n);
    }

    /** A static nested helper. */
    static class Helper {
        int count;

        void assist(int n) {
            count += n;
            Inner inner = new Inner();
            inner.poke();
        }

        class Inner {
            void poke() {
                count++;
            }
        }
    }

    interface Visitor<V> {
        int VERSION = 2;

        V visit(Shape s);

        default V fallback() {
            return null;
        }
    }

    enum Color implements Visitor<String> {
        RED("r") {
            @Override
            public String visit(Shape s) {
                return "red";
            }
        },
        GREEN("g");

        private final String code;

        Color(String code) {
            this.code = code;
        }

        public String visit(Shape s) {
            return code;
        }
    }

    record Point(int x, int y) implements Comparable<Point> {
        Point {
            if (x < 0) {
                throw new IllegalArgumentException();
            }
        }

        public int compareTo(Point other) {
            return Integer.compare(x, other.x());
        }
    }

    @interface Marker {
        String value() default "";
    }
}
