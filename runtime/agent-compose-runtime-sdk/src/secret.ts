export const secret = {
  get(name: string): string | undefined {
    const normalizedName = name.trim();
    if (!normalizedName) {
      throw new Error("secret name is required");
    }
    return process.env["SECRET_" + normalizedName];
  },
};
