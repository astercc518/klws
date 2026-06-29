import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // Emit a self-contained server bundle (.next/standalone/server.js) for a slim
  // production Docker image.
  output: "standalone",
};

export default nextConfig;
