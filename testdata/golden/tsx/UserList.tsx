import React, { useState } from "react";
import { Avatar } from "./Avatar";
import * as UI from "@acme/ui";
import type { User } from "../models/user";

interface Props {
  users: User[];
  onSelect?: (u: User) => void;
}

export function UserList({ users, onSelect }: Props) {
  const [selected, setSelected] = useState<User | null>(null);
  const handleClick = (u: User) => {
    setSelected(u);
    onSelect?.(u);
  };
  return (
    <UI.Panel title="Users">
      {users.map((u) => (
        <Row key={u.id} user={u} active={u === selected} onClick={handleClick} />
      ))}
    </UI.Panel>
  );
}

const Row = ({ user, active, onClick }: RowProps) => (
  <li className={active ? "active" : ""} onClick={() => onClick(user)}>
    <Avatar src={user.avatarUrl} />
    {user.name}
  </li>
);

type RowProps = { user: User; active: boolean; onClick: (u: User) => void };

export default class Counter extends React.Component<{ start: number }, { n: number }> {
  state = { n: this.props.start };

  increment = () => this.setState({ n: this.state.n + 1 });

  render() {
    return <button onClick={this.increment}>{this.state.n}</button>;
  }
}
