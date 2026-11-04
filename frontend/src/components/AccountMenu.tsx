"use client";

import { api } from "@/lib/api";
import { Icon } from "@/shared/components/atoms/Icon";
import { Avatar } from "@/shared/components/atoms/Avatar";
import { Button } from "@/shared/components/molecules/Button";
import { Popover } from "@/shared/components/molecules/Popover";
import { Appearance } from "@/shared/components/types";

import { useRouter } from "next/navigation";

/** Self-hosted installs have one shared admin login, so the menu only offers sign-out. */
export function AccountMenu() {
  const router = useRouter();

  return (
    <Popover>
      <Popover.Trigger>
        <button type="button" aria-label="Account menu" className="rounded-full">
          <Avatar name="Admin" className="size-8" />
        </button>
      </Popover.Trigger>
      <Popover.Content align="start" className="w-48 p-1">
        <Button
          appearance={Appearance.Hollow}
          className="h-8 w-full justify-start gap-2 px-2 text-body"
          onClick={async () => {
            await api.auth.logout().catch(() => undefined);
            router.replace("/login");
          }}
        >
          <Icon type="LogOut" className="size-4 text-wv-secondary" />
          Log out
        </Button>
      </Popover.Content>
    </Popover>
  );
}
