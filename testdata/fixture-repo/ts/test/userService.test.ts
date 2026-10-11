import { describe, expect, it } from "vitest";
import { User } from "../src/models/user";
import { UserService } from "../src/services/userService";

describe("UserService", () => {
  it("greets a known user", () => {
    const svc = new UserService();
    svc.add(new User("u1", "ada"));
    expect(svc.greet("u1")).toBe("Hello, Ada (member)");
  });

  it("promotes to admin", () => {
    const svc = new UserService();
    svc.add(new User("u2", "linus"));
    expect(svc.promote("u2")?.role()).toBe("admin");
  });
});
