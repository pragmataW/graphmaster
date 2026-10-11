// A user's own plugin; graphmaster must never touch it.
export const NotifyPlugin = async ({ $ }) => ({
  event: async ({ event }) => {
    if (event.type === "session.idle") await $`osascript -e 'display notification "done" with title "opencode"'`
  },
})
