import { Button } from "./Button";

export function Toolbar({ onSave }) {
  return (
    <div className="toolbar">
      <Button label="Save" onClick={onSave} />
      <Icons.Gear />
    </div>
  );
}
