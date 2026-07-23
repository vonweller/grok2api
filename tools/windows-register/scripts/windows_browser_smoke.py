import asyncio
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from playwright.async_api import async_playwright

from grok_register.register import find_chrome


async def main():
    executable = find_chrome()
    async with async_playwright() as playwright:
        browser = await playwright.chromium.launch(
            executable_path=executable,
            headless=True,
        )
        try:
            page = await browser.new_page()
            await page.goto(
                "data:text/html,<title>Windows smoke</title><main id='ready'>ok</main>"
            )
            result = {
                "browser": executable,
                "title": await page.title(),
                "ready": await page.locator("#ready").text_content(),
            }
            if result["title"] != "Windows smoke" or result["ready"] != "ok":
                raise RuntimeError("unexpected browser smoke-test result")
            print(json.dumps(result, ensure_ascii=True))
        finally:
            await browser.close()


if __name__ == "__main__":
    asyncio.run(main())
