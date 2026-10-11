import { Admin, User } from "@app/models/user";
import { formatName } from "../utils/format";

export class UserService {
  private users = new Map<string, User>();

  add(user: User): void {
    this.users.set(user.id, user);
  }

  find(id: string): User | undefined {
    return this.users.get(id);
  }

  greet(id: string): string {
    const user = this.find(id);
    if (!user) {
      return "Hello, stranger";
    }
    return `Hello, ${formatName(user.name)} (${user.role()})`;
  }

  promote(id: string): Admin | undefined {
    const user = this.find(id);
    return user ? new Admin(user.id, user.name) : undefined;
  }
}
