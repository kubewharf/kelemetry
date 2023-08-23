#!/usr/bin/env node

const fs = require("fs/promises")
const path = require("path")

const pt = require("puppeteer")

const traceId = process.env.MODE_TRACE_ID
const testDir = process.env.TEST_DIR
const validate = require(path.join(testDir, "validate.js"))

const screenshotPath = process.env.SCREENSHOT_PATH

;(async () => {
    const browser = await pt.launch({headless: "new"})

    console.log(`Loading trace ${testDir} at http://localhost:16686/trace/${traceId}/`)

    const page = await browser.newPage()
    await page.goto(`http://localhost:16686/trace/${traceId}/`)
    await page.setViewport({width: 1920, height: 1080})

    await validate(page)

    await new Promise(resolve => setTimeout(resolve, 1000))

    if(screenshotPath) {
        console.log("dump screenshot")
        await page.screenshot({path: screenshotPath})
    }

    await browser.close()
    console.log("ok")
})()
