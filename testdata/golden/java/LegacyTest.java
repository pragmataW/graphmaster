package com.example.legacy;

import junit.framework.TestCase;
import org.junit.jupiter.params.ParameterizedTest;

public class LegacyTest extends TestCase {
    public void testFetchItem() {
        assertTrue(new Fetcher().fetchItem());
    }

    public void helper() {}

    @org.junit.Test
    public void qualifiedAnnotation() {}

    @ParameterizedTest
    void parameterized(int n) {}
}
