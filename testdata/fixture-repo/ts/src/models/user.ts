export interface Entity {
  id: string;
}

export type Role = "member" | "admin";

export class User implements Entity {
  constructor(
    public id: string,
    public name: string,
  ) {}

  role(): Role {
    return "member";
  }
}

export class Admin extends User {
  role(): Role {
    return "admin";
  }

  canDelete(target: User): boolean {
    return target.role() !== "admin";
  }
}
