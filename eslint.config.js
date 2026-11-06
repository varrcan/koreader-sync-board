// Общий flat config ESLint для всех пакетов монорепозитория.
import js from "@eslint/js";
import { defineConfig, globalIgnores } from "eslint/config";
import reactHooks from "eslint-plugin-react-hooks";
import globals from "globals";
import tseslint from "typescript-eslint";

export default defineConfig(
  globalIgnores([
    "**/node_modules/",
    "**/dist/",
    "server/",
    // Генерируется openapi-typescript из api/openapi.yaml
    "packages/api-client/src/schema.d.ts",
  ]),
  js.configs.recommended,
  tseslint.configs.recommended,
  {
    files: ["**/*.{ts,tsx}"],
    languageOptions: {
      globals: globals.browser,
    },
  },
  {
    files: ["**/*.tsx"],
    extends: [reactHooks.configs.flat.recommended],
  },
  {
    files: ["*.js", "**/*.config.{js,ts}", "**/scripts/*.js"],
    languageOptions: {
      globals: globals.node,
    },
  },
);
