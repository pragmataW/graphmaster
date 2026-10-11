import { UserService } from "@app/services/userService";

interface UserCardProps {
  service: UserService;
  userId: string;
}

export function UserCard({ service, userId }: UserCardProps) {
  const greeting = service.greet(userId);
  return (
    <div className="user-card">
      <Badge label={greeting} />
    </div>
  );
}

function Badge({ label }: { label: string }) {
  return <span className="badge">{label}</span>;
}
