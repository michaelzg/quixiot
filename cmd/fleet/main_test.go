package main

import "testing"

func TestRunRejectsNonPositiveCount(t *testing.T) {
	err := run([]string{"--count=0"})
	if err == nil {
		t.Fatal("run: expected error for count=0")
	}
}

func TestRunRejectsNegativeStagger(t *testing.T) {
	err := run([]string{"--count=1", "--stagger-ms=-1"})
	if err == nil {
		t.Fatal("run: expected error for negative stagger")
	}
}
