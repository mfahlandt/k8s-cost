import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// base is set for GitHub Pages project sites (https://<org>.github.io/k8s-cost/).
// Override with VITE_BASE when deploying elsewhere.
export default defineConfig({
  plugins: [react()],
  base: process.env.VITE_BASE || "/k8s-cost/",
});

